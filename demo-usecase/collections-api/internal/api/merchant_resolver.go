package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/mopay/collections-api/internal/merchantclient"
	"github.com/mopay/collections-api/internal/platform"
	"github.com/mopay/collections-api/internal/store"
)

// resolveMerchant returns a merchant, with merchant-api as the system of record and the
// local projection as the fallback.
//
// The order matters and is deliberate:
//
//  1. Ask merchant-api. On success, refresh the local projection so the foreign keys
//     collections depend on exist, and so `ledger` and settlement - which read the local
//     table inside a transaction - see current terms.
//  2. On a definitive 404, the merchant does not exist. Fail.
//  3. On a TRANSPORT failure, fall back to the local projection. C-1.5 already makes the
//     ledger a fail-closed dependency of intake, deliberately. Making merchant-api a
//     second one would mean an outage in another project stops payments, which is a worse
//     trade than serving a merchant's terms from a copy that is at most one TTL stale.
func (a *API) resolveMerchant(ctx context.Context, merchantID string) (*store.Merchant, error) {
	log := platform.LoggerFrom(ctx, a.log)

	m, fromCache, err := a.merchant.Get(ctx, merchantID)
	switch {
	case err == nil:
		if !fromCache {
			a.reg.IncCounter("merchant_lookups_total",
				"Merchant resolutions by source.", platform.Labels{"source": "merchant_api"})
			if perr := a.st.UpsertMerchantProjection(ctx, projection(m)); perr != nil {
				// The projection failing is not fatal to this request, but it will break
				// the next collection insert (foreign key), so it is an error not a warning.
				log.Error("merchant projection failed to write",
					slog.String(platform.FieldMerchant, merchantID),
					slog.String("error", perr.Error()))
			}
		} else {
			a.reg.IncCounter("merchant_lookups_total",
				"Merchant resolutions by source.", platform.Labels{"source": "cache"})
		}
		return a.st.Merchant(ctx, merchantID)

	case errors.Is(err, merchantclient.ErrNotFound):
		a.reg.IncCounter("merchant_lookups_total",
			"Merchant resolutions by source.", platform.Labels{"source": "not_found"})
		return nil, platform.Errorf(http.StatusNotFound, platform.CodeNotFound,
			"merchant %s does not exist in the merchant system of record", merchantID)

	default:
		// Degraded, not failed. Say so loudly enough to alert on.
		log.Warn("merchant-api unreachable; serving merchant from the local projection",
			slog.String(platform.FieldMerchant, merchantID),
			slog.String("error", err.Error()))
		a.reg.IncCounter("merchant_lookups_total",
			"Merchant resolutions by source.", platform.Labels{"source": "projection_fallback"})
		return a.st.Merchant(ctx, merchantID)
	}
}

func projection(m *merchantclient.Merchant) store.ProjectedMerchant {
	p := store.ProjectedMerchant{
		ID:                 m.ID,
		Name:               m.Name,
		Country:            m.Country,
		DataRegion:         m.DataRegion,
		PayoutBank:         m.PayoutBank,
		PayoutAccountRef:   m.PayoutAccountMask,
		PayoutAccountName:  m.PayoutAccountName,
		FloatLimitMinor:    m.FloatLimitMinor,
		FloatLimitCurrency: m.FloatLimitCurrency,
	}
	for _, f := range m.FeeSchedules {
		p.FeeSchedules = append(p.FeeSchedules, store.ProjectedFeeSchedule{
			Channel:      f.Channel,
			PercentageBP: f.PercentageBP,
			FixedMinor:   f.FixedMinor,
		})
	}
	return p
}
