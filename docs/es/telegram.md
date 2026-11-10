# Telegram

La [guía completa de Telegram (EN)](../en/telegram.md) describe los cambios del código aún no publicados: temas privados, tarjetas de día y tiempo, alias, citas y callbacks personales.

Configure el token y `telegram.allowed_ids`; active los temas en BotFather. Un mensaje normal en el chat raíz inicia un tema; `/topic` lista los conocidos. `/clear` y `/compact` afectan al contexto actual. `/topic pause` pausa Iulita, no cierra el tema; `/topic delete` requiere confirmación y cancela trabajo local asociado sin borrar tareas externas.

Use `/day`, `/day text`, `/weather London 4` o `/weather text London 4`. `telegram.day_widgets` y `telegram.day_actions` están activados por defecto, también al actualizar configuraciones sin estas claves; un valor false explícito se conserva y los cambios requieren reinicio. Las acciones necesitan confirmación del propietario.

Las respuestas se almacenan temporalmente hasta la comprobación final de privacidad; no se publican fragmentos sin comprobar. Un fallo del envío privado de datos sensibles no causa un envío ordinario de esos datos. La combinación de mensajes separa usuarios y temas. Los widgets en producción son privados; inline/shared requieren el servidor de pruebas de Telegram. Consulte la guía EN para voz, rich quotes, disponibilidad, configuración y recuperación.

![English previews of day, weather and shared-result cards](/screenshots/telegram/card-family-en.png)

*Illustrative browser previews of production templates with sample English data, not native Telegram captures. Shared-result widgets require Telegram's test server.*
