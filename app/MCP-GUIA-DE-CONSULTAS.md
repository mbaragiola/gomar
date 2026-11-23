# Qué preguntarle a Gomar desde el chat

Reemplazá lo que está entre corchetes por los datos de tu operación. Decí qué
sucursal querés consultar y usá día, mes y año cuando la fecha importe. Si hay
dos coincidencias, pedile al chat que te pregunte antes de elegir. No hace
falta copiar identificadores técnicos ni pegar claves o archivos en el chat.

## Consultar con la clave de lectura de la sucursal

**Ventas de caja**

> ¿Cuánto vendimos por caja en [sucursal] el mes pasado? Decime qué mes tomaste y no sumes otras sucursales.

Podés pedir este mes o el anterior, según el calendario de Buenos Aires.
El total es en pesos argentinos y solo incluye ventas registradas en la
caja de Gomar, no delivery ni facturas.

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
