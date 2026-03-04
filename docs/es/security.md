# Seguridad

## Autenticacion

### JWT

- **Algoritmo**: HMAC-SHA256
- **TTL del token de acceso**: 24 horas
- **TTL del token de refresco**: 7 dias
- **Claims**: `user_id`, `username`, `role`
- **Secreto**: 32 bytes hex auto-generados si no esta configurado
- **Almacenamiento**: llavero (macOS Keychain / Linux SecretService) o variable de entorno `IULITA_JWT_SECRET`

### Contrasenas

- **Hashing**: bcrypt con costo predeterminado
- **Bootstrap**: el primer usuario obtiene una contrasena aleatoria con `MustChangePass: true`
- **Panel de control**: endpoint de cambio de contrasena en `POST /api/auth/change-password`

### Middleware

1. `FiberMiddleware` — valida `Authorization: Bearer <token>`, almacena claims en los locals de fiber
2. `AdminOnly` — verifica `role == admin`, devuelve 403 si no lo es

## Gestion de Secretos

### Capas de Almacenamiento

| Secreto | Variable de Entorno | Llavero | Respaldo en Archivo |
|---------|--------------------|---------|--------------------|
| Clave API de Claude | `IULITA_CLAUDE_API_KEY` | `claude-api-key` | — |
| Token de Telegram | `IULITA_TELEGRAM_TOKEN` | `telegram-token` | — |
| Secreto JWT | `IULITA_JWT_SECRET` | `jwt-secret` | auto-generar |
| Clave de cifrado de config | `IULITA_CONFIG_KEY` | `config-encryption-key` | archivo `encryption.key` |

**Orden de resolucion**: variable de entorno → llavero → respaldo en archivo → auto-generar.

### Cifrado de Configuracion (AES-256-GCM)

Las sobreescrituras de configuracion en tiempo de ejecucion en la base de datos pueden cifrarse:

- **Algoritmo**: AES-256-GCM (cifrado autenticado)
- **Nonce**: 12 bytes, generado aleatoriamente por cifrado
- **Formato**: `base64(nonce ‖ ciphertext)`
- **Auto-cifrado**: claves declaradas como `secret_keys` en manifiestos SKILL.md
- **Seguridad de la API**: el panel nunca devuelve valores descifrados para claves secretas
- **Rechazar marcadores**: valores `"***"` o vacios rechazados para claves secretas

## Proteccion SSRF

Todas las solicitudes HTTP salientes (web fetch, busqueda web, habilidades externas) pasan por proteccion SSRF.

### Rangos de IP Bloqueados

| Rango | Tipo |
|-------|------|
| `10.0.0.0/8` | RFC1918 privada |
| `172.16.0.0/12` | RFC1918 privada |
| `192.168.0.0/16` | RFC1918 privada |
| `100.64.0.0/10` | NAT de grado carrier (RFC 6598) |
| `fc00::/7` | IPv6 Unique Local |
| `127.0.0.0/8` | Loopback |
| `::1/128` | Loopback IPv6 |
| `169.254.0.0/16` | Link-local |
| `fe80::/10` | Link-local IPv6 |
| Rangos multicast | Todos |

Las direcciones IPv6 mapeadas a IPv4 se normalizan a IPv4 antes de verificar.

### Proteccion de Doble Capa (Sin Proxy)

**Capa 1 — DNS previo al vuelo**: Antes de conectar, todas las IPs del hostname se resuelven. Si alguna IP es privada, la conexion se rechaza.

**Capa 2 — Control en tiempo de conexion**: Una funcion `net.Dialer.Control` verifica la IP realmente resuelta al momento de conectar. Esto captura **ataques de DNS rebinding** donde un hostname resuelve a una IP publica durante la verificacion previa pero se reasigna a una IP privada antes de la conexion real.

### Ruta con Proxy

Cuando hay un proxy configurado (`proxy.url`), el enfoque basado en dialer no puede usarse (el proxy mismo puede tener una IP privada en clusters de Kubernetes). En su lugar:

- `ssrfTransport.RoundTrip` realiza solo verificacion previa a nivel de URL
- Se permite la conexion del proxy a IPs privadas (intencional para proxies internos del cluster)
- Las URLs objetivo a IPs privadas siguen bloqueadas

### Deteccion Activa de Proxy

`isProxyActive()` realmente llama a la funcion de proxy con una solicitud de prueba (no solo `Proxy != nil`), porque `http.DefaultTransport` siempre tiene `Proxy = ProxyFromEnvironment` establecido.

## Niveles de Aprobacion de Herramientas

| Nivel | Comportamiento | Habilidades |
|-------|----------------|-------------|
| `ApprovalAuto` | Ejecutar inmediatamente | La mayoria de las habilidades (predeterminado) |
| `ApprovalPrompt` | El usuario debe confirmar | Ejecutor Docker |
| `ApprovalManual` | El administrador debe confirmar | Shell exec |

### Flujo

1. La habilidad declara su nivel via la interfaz `ApprovalDeclarer`
2. Antes de la ejecucion, el asistente verifica `registry.ApprovalLevelFor(toolName)`
3. Para `Prompt`/`Manual`: almacena la llamada a herramienta pendiente en `approvalStore`
4. Envia prompt de confirmacion al usuario
5. Devuelve `"awaiting approval"` al LLM (no bloqueante)
6. El siguiente mensaje del usuario se verifica contra el vocabulario de aprobacion sensible al idioma
7. Si se aprueba: ejecuta la llamada a herramienta almacenada, devuelve resultado
8. Si se rechaza: devuelve "cancelled"

### Vocabulario Sensible al Idioma

Las palabras de aprobacion se definen en los 6 catalogos de idioma e incluyen ingles como respaldo:

```
# Afirmativo en ruso:
да, д, ок, подтвердить, подтверждаю, yes, y, ok, confirm

# Negativo en hebreo:
לא, ביטול, בטל, no, n, cancel
```

## Seguridad de Telegram

- **Lista blanca de usuarios**: `telegram.allowed_ids` restringe quien puede chatear con el bot
- **Lista blanca vacia**: permite todos los usuarios (se registra advertencia)
- **Limitacion de tasa**: limitador de tasa por ventana deslizante por chat

## Seguridad de Shell Exec

La habilidad `shell_exec` tiene la seguridad mas estricta:

- **Nivel de aprobacion**: `ApprovalManual` (requiere confirmacion del administrador)
- **Solo lista blanca**: solo los ejecutables en `AllowedBins` pueden ejecutarse
- **Rutas prohibidas**: lista configurable de rutas que no pueden aparecer en argumentos
- **Recorrido de ruta**: `..` en argumentos se rechaza
- **Limite de salida**: maximo 16KB
- **Directorio de trabajo**: `os.TempDir()` (no el directorio del proyecto)

## Limitacion de Tasa

### Limitador de Tasa por Chat

Ventana deslizante: rastrea marcas de tiempo por `chatID`. Si el conteo de mensajes dentro de la `window` excede el `rate`, el mensaje se rechaza.

### Limitador de Acciones Global

Ventana fija: conteo total de acciones LLM/herramientas por hora en todos los chats. Se reinicia automaticamente en el limite de la ventana.

## Seguimiento de Costos

- **En memoria**: costo diario rastreado con mutex, se reinicia automaticamente en el limite del dia
- **Persistente**: `IncrementUsageWithCost` guarda en la tabla `usage_stats`
- **Limite diario**: `cost.daily_limit_usd` (0 = ilimitado)
- **Precios por modelo**: `config.ModelPrice{InputPerMillion, OutputPerMillion}`

## Seguridad CI/CD

- **Hook pre-commit**: bloquea secretos via [gitleaks](https://github.com/gitleaks/gitleaks)
- **CI**: la accion gitleaks escanea todos los commits
- **CodeQL**: consultas extendidas de seguridad para Go y JavaScript/TypeScript (cuando el repositorio es publico)
- **Dependencias**: alertas de Dependabot (habilitar en la configuracion de GitHub)

## Seguridad de Habilidades Externas

- **Validacion de slug**: `^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$` — previene recorrido de ruta
- **Verificacion de checksum**: SHA-256 para descargas remotas
- **Validacion de aislamiento**: las habilidades deben declarar nivel de aislamiento, verificado contra flags de configuracion
- **Deteccion de codigo**: rechaza habilidades con archivos de codigo a menos que esten correctamente aisladas
- **Escaneo de inyeccion de prompts**: advierte sobre patrones sospechosos en el cuerpo de la habilidad
- **Tamano maximo de archivo**: configurable (predeterminado 50MB para ClawhHub)
