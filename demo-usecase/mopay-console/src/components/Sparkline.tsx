/**
 * S-1's 24-hour sparkline, as inline SVG.
 *
 * NFR-2 assumes mobile and constrained bandwidth, so this is ~40 lines of SVG rather
 * than a charting dependency.
 */
export function Sparkline({
  points, width = 560, height = 48,
}: {
  points: { hour: string; count: number }[]
  width?: number
  height?: number
}) {
  if (points.length === 0) {
    return <div className="muted" style={{ fontSize: 12 }}>No activity in this window.</div>
  }

  const max = Math.max(...points.map((p) => p.count), 1)
  const step = points.length > 1 ? width / (points.length - 1) : width
  const y = (c: number) => height - (c / max) * (height - 4) - 2

  const line = points.map((p, i) => `${i === 0 ? 'M' : 'L'} ${(i * step).toFixed(1)} ${y(p.count).toFixed(1)}`).join(' ')
  const area = `${line} L ${width} ${height} L 0 ${height} Z`

  return (
    <svg viewBox={`0 0 ${width} ${height}`} width="100%" height={height}
         preserveAspectRatio="none" role="img"
         aria-label={`Collections per hour, peak ${max}`}>
      <path d={area} fill="rgba(79,140,255,.16)" />
      <path d={line} fill="none" stroke="var(--accent)" strokeWidth="1.5"
            strokeLinejoin="round" strokeLinecap="round" vectorEffect="non-scaling-stroke" />
    </svg>
  )
}
