const BUSINESS_TIME_ZONE = 'America/Argentina/Buenos_Aires'

export function money(value: string | null, currency: string): string {
  if (value === null) return 'Sin informar'
  const match = /^(-?)(\d+)(?:\.(\d{1,2}))?$/.exec(value)
  if (!match) return value
  const [, sign, rawInteger, rawFraction = ''] = match
  const integer = rawInteger.replace(/^0+(?=\d)/, '').replace(/\B(?=(\d{3})+(?!\d))/g, '.')
  const fraction = rawFraction.padEnd(2, '0')
  const symbol = currency === 'USD' ? 'US$' : currency === 'ARS' ? '$' : currency
  return `${sign}${symbol} ${integer},${fraction}`
}

export function localDate(value: string): string {
  const [year, month, day] = value.split('-').map(Number)
  return new Intl.DateTimeFormat('es-AR').format(new Date(year, month - 1, day))
}

export function localDateTime(value: string): string {
  return new Intl.DateTimeFormat('es-AR', {
    timeZone: BUSINESS_TIME_ZONE,
    dateStyle: 'short',
    timeStyle: 'short',
    hour12: false,
  }).format(new Date(value))
}

export function todayInBuenosAires(now = new Date()): string {
  const parts = new Intl.DateTimeFormat('en-CA', {
    timeZone: BUSINESS_TIME_ZONE,
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
  }).formatToParts(now)
  const values = Object.fromEntries(parts.map((part) => [part.type, part.value]))
  return `${values.year}-${values.month}-${values.day}`
}
