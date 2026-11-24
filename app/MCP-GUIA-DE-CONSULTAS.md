# Qué preguntarle a Gomar desde el chat

Reemplazá lo que está entre corchetes por los datos de tu operación. Decí qué
sucursal querés consultar y usá día, mes y año cuando la fecha importe. Si hay
dos coincidencias, pedile al chat que te pregunte antes de elegir. No hace
falta copiar identificadores técnicos ni pegar claves o archivos en el chat.

## Consultar con la clave de lectura de la sucursal

**Ventas de caja**

> ¿Cuánto vendimos por caja en [sucursal] el mes pasado? Decime qué mes tomaste y no sumes otras sucursales.

Podés pedir este mes o el anterior. El mes se determina por la hora en que
Gomar registró cada venta, en Buenos Aires, no por el reloj de la caja.
El total es en pesos argentinos y no incluye delivery ni facturas.

> En [sucursal], para mi venta con referencia [referencia visible en Caja], ¿a qué hora la registró Gomar y qué hora informó la caja?

La hora informada por la caja puede faltar y no está verificada. Ninguno de
los dos horarios acredita un pago ni determina cuándo ocurrió la venta.

**Reservas**

> Para el [día, mes y año] en [sucursal], ¿cuántas solicitudes hay pendientes, confirmadas y canceladas? No necesito nombres.
>
> ¿Qué horarios ofrece [sucursal] para 4 personas el [fecha futura completa]? Solo quiero consultar, no reservar.

Son consultas para quienes administran esa sucursal. Los horarios solo se
pueden consultar si las reservas públicas y las de ese local están habilitadas.
Elegí desde hoy hasta 60 días adelante, inclusive. Un horario es una opción
del momento, no una reserva ni una garantía.

**Stock**

> En [sucursal], ¿qué saldo y mínimo tiene [producto]? Resumime cuántos movimientos hubo y cuántas unidades netas cambiaron por tipo entre el [fecha de inicio] y el [fecha de fin].

Usá hasta 31 días en Buenos Aires. La sucursal debe tener stock habilitado
y tu rol debe permitir consultarlo. Gomar muestra el saldo estimado que tiene
registrado, no un conteo físico nuevo.

**Comprobantes**

> Buscá la Nota de crédito sin letra, punto de venta 00012 y número 00000345 en [sucursal]. Si hay varias coincidencias, mostrame cuáles para elegir en Gomar.
>
> Mostrame los comprobantes que todavía están en borrador en [sucursal]. No los marques como revisados.

Usá los cuatro datos tal como figuran en el comprobante. El punto de
venta impreso no es la sucursal. La búsqueda no abre adjuntos, cambia
datos ni marca el comprobante como revisado.

**Cuentas a pagar**

> Mostrame las cuentas a pagar de [sucursal] para que elija una. Una vez que la identifiquemos, explicame cómo cambió su saldo: alta, aplicaciones de pagos y reversiones, con la fecha si está registrada.

Hace falta el permiso de cuentas a pagar. El historial explica el saldo
interno de una cuenta de esa sucursal, no confirma que se movió dinero en el
banco. Si Gomar deniega una consulta, no lo tomes como saldo cero ni como
una lista vacía.

## Proponer un cambio y revisarlo en Gomar

La clave de lectura no sirve para proponer ni aprobar cambios. En
**Chat y propuestas**, emití una clave aparte para la acción concreta, si
está habilitada y tu rol la permite. El chat solo deja la propuesta
pendiente. Volvé a la misma sesión de Gomar que emitió la clave: verificá la
sucursal, el registro y los valores exactos antes de aprobar o rechazar.
Confirmar en el chat no aplica el cambio.

> Prepará una propuesta para cambiar a [cantidad] unidades el mínimo de stock de [producto], que ya tiene seguimiento activo en [sucursal]. No cambies el saldo ni el seguimiento.
>
> Para el borrador sin revisar que acabamos de identificar en [sucursal], prepará una corrección solo del punto de venta impreso a [punto de venta correcto]. Dejá el número, los importes y el local sin cambios. Si no está claro cuál es el borrador, preguntame primero.

Son ejemplos de pedidos, no un registro de conversaciones ejecutadas. Para
ver el alcance completo, consultá la [cobertura de MCP](MCP-COVERAGE.md).


Para un producto con ID conocido: «¿Cuántas unidades quedan del producto 84
en esta sucursal? ¿Está bajo el mínimo o está apagado el control de stock?».
`gomar_read_stock_product` muestra el saldo registrado, el mínimo y si el
control está activo tanto en el local como para el producto. Si está
apagado en cualquiera de los dos, no calcula una alerta. Sólo consulta
productos con stock configurado y no cambia el inventario.


Para revisar alertas: «Mostrame los productos con stock negativo o bajo
el mínimo en esta sucursal». Usá `gomar_list_stock` con `alert: any`,
`negative` (saldo negativo) o `low` (saldo no negativo en o debajo del
mínimo). El filtro solo incluye productos con control activo en locales
con control activo; si el local lo tiene apagado, la lista filtrada queda
vacía. Para seguir, repetí el filtro con el `next_product_id` de la página
anterior. Si cambiás de filtro, omití el cursor y empezá de nuevo.
El saldo es estimado y puede no incluir ventas de caja sin sincronizar.
