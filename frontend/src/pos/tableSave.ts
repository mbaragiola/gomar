import { maxTotal, positive, type Context, type Line } from './model'

type TableItem = Pick<Line, 'producto_id' | 'revision' | 'cantidad' | 'nombre'>

export interface TableSave extends Context {
  id: string
  table: number
  items: TableItem[]
  total: number
}

export interface TableCheck {
  id_cuenta: string
  id_solicitud?: string
  estado: 'sin_cobro' | 'retirada' | 'con_registros' | 'finalizada'
  mesa: number
  total_centavos: number
  items: Line[]
}

export function tableSaveRequest(intent: TableSave) {
  const items = intent.items.map(({ producto_id, revision, cantidad }) => ({ producto_id, revision, cantidad }))
  return { id_solicitud: intent.id, actor_id: intent.actor, mesa: intent.table, items }
}

export function isTableCheck(check: TableCheck | null | undefined, table: number): check is TableCheck {
  return !!check && check.mesa === table && positive(check.total_centavos) && check.total_centavos <= maxTotal
    && typeof check.id_cuenta === 'string' && /^[1-9][0-9]{0,18}$/.test(check.id_cuenta)
    && ['sin_cobro', 'retirada', 'con_registros', 'finalizada'].includes(check.estado)
    && Array.isArray(check.items) && check.items.length > 0
}

export function matchesTableSave(intent: TableSave, check: TableCheck | null | undefined): check is TableCheck {
  return isTableCheck(check, intent.table) && check.id_solicitud === intent.id
    && check.total_centavos === intent.total && check.items.length === intent.items.length
    && intent.items.every((line, index) => {
      const item = check.items[index]
      return item?.producto_id === line.producto_id && item.revision === line.revision && item.cantidad === line.cantidad
        && typeof item.nombre === 'string' && !!item.nombre && positive(item.precio_centavos)
    }) && check.items.reduce((total, item) => total + item.cantidad * item.precio_centavos, 0) === intent.total
}

export function matchesTableSaveRejection(intent: TableSave, value: unknown) {
  if (!value || typeof value !== 'object') return false
  const receipt = value as { id_solicitud?: unknown; mesa?: unknown; rechazo?: unknown }
  return receipt.id_solicitud === intent.id && receipt.mesa === intent.table
    && (receipt.rechazo === 'mesa_ocupada' || receipt.rechazo === 'catalogo_cambio')
}
