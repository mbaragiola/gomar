const zone = 'America/Argentina/Buenos_Aires'

export const reservationTimes = Array.from({ length: 16 }, (_, index) => {
  const minutes = index * 90
  return `${String(Math.trunc(minutes / 60)).padStart(2, '0')}:${String(minutes % 60).padStart(2, '0')}`
})

export function reservationSlotUTC(day: string, time: string): string {
  if (!/^\d{4}-\d{2}-\d{2}$/.test(day) || !reservationTimes.includes(time)) return ''
  const noon = new Date(`${day}T12:00:00Z`)
  if (Number.isNaN(noon.getTime()) || noon.toISOString().slice(0, 10) !== day) return ''
  const parts = new Intl.DateTimeFormat('en-US', { timeZone: zone, timeZoneName: 'longOffset' }).formatToParts(noon)
  const offset = parts.find((part) => part.type === 'timeZoneName')?.value
  if (!offset || (offset !== 'GMT' && !/^GMT[+-]\d{2}:\d{2}$/.test(offset))) return ''
  const result = new Date(`${day}T${time}:00${offset === 'GMT' ? 'Z' : offset.slice(3)}`)
  const local = new Intl.DateTimeFormat('sv-SE', {
    timeZone: zone, year: 'numeric', month: '2-digit', day: '2-digit',
    hour: '2-digit', minute: '2-digit', hourCycle: 'h23',
  }).formatToParts(result)
  const value = Object.fromEntries(local.map((part) => [part.type, part.value]))
  if (`${value.year}-${value.month}-${value.day} ${value.hour}:${value.minute}` !== `${day} ${time}`) return ''
  return result.toISOString().replace('.000Z', 'Z')
}
