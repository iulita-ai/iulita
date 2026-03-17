# Orquestacion Multi-Agente

Iulita soporta la ejecucion paralela de sub-agentes para descomponer tareas complejas. El LLM decide autonomamente cuando usar la orquestacion basandose en la complejidad de la tarea.

## Vision General

La habilidad `orchestrate` lanza multiples sub-agentes especializados en paralelo. Cada sub-agente ejecuta un bucle agentivo simplificado con su propio prompt de sistema, subconjunto de herramientas y enrutamiento opcional de proveedor LLM. Los resultados se recopilan y devuelven al asistente principal como un informe markdown estructurado.

## Arquitectura

```
Mensaje del usuario
    │
    ▼
Asistente (bucle agentivo principal)
    │ decide que se necesita orquestacion
    │ llama a la herramienta orchestrate con especificaciones de agentes
    ▼
Habilidad Orchestrate
    │ valida profundidad (maximo 1)
    │ construye Budget desde configuracion + entradas
    ▼
Orquestador (ejecucion paralela via errgroup)
    ├── Runner (agent_1: researcher) ──→ LLM ←→ Herramientas
    ├── Runner (agent_2: analyst)    ──→ LLM ←→ Herramientas
    └── Runner (agent_3: planner)    ──→ LLM ←→ Herramientas
         │
         │ presupuesto atomico compartido de tokens
         │ timeout + contexto por agente
         │ eventos de estado → canales
         ▼
    AgentResults recopilados
    │ formateados como markdown
    ▼
El asistente continua con la salida de la orquestacion
```

## Tipos de Agente

| Tipo | Enfoque del Prompt | Herramientas Predeterminadas | Route Hint |
|------|-------------------|------------------------------|------------|
| `researcher` | Recopilar informacion, buscar en la web | `web_search`, `webfetch` | — |
| `analyst` | Identificar patrones, anomalias, insights | todas | — |
| `planner` | Descomponer objetivos en pasos ordenados | `datetime` | — |
| `coder` | Escribir, revisar, depurar codigo | todas | — |
| `summarizer` | Condensar entrada a puntos esenciales | todas | `ollama` |
| `generic` | Proposito general | todas | — |

## Sistema de Presupuesto

Todos los agentes comparten un unico contador atomico `atomic.Int64`. El limite es **suave** por diseno — multiples agentes pueden pasar la verificacion simultaneamente antes de que alguno deduzca tokens.

| Parametro | Predeterminado | Sobreescritura |
|-----------|---------------|----------------|
| Turnos maximos | 10 | `Budget.MaxTurns` |
| Timeout | 60s | `Budget.Timeout` o entrada `timeout` |
| Agentes paralelos maximos | 5 | `Budget.MaxAgents` o configuracion |
| Presupuesto compartido de tokens | ilimitado | `Budget.MaxTokens` o entrada `max_tokens` |

## Control de Profundidad

Los sub-agentes no pueden generar mas sub-agentes (`MaxDepth = 1`). Reforzado por verificacion de contexto y filtrado del instrumento `orchestrate` de las listas de herramientas.

## Seguridad

- Las habilidades con ApprovalManual/ApprovalPrompt se filtran de los sub-agentes
- Listas blancas de herramientas opcionales por agente
- El instrumento `orchestrate` siempre se excluye de los sub-agentes

## Eventos de Estado

| Evento | Cuando | Campos |
|--------|--------|--------|
| `orchestration_started` | Antes de lanzar agentes | `agent_count` |
| `agent_started` | Por agente, antes de ejecutar | `agent_id`, `agent_type` |
| `agent_progress` | Por agente, despues de cada turno LLM | `agent_id`, `turn` |
| `agent_completed` | Por agente, en exito | `agent_id`, `tokens`, `duration_ms` |
| `agent_failed` | Por agente, en error | `agent_id`, `error` |
| `orchestration_done` | Despues de que todos terminen | `success_count`, `total_tokens`, `duration_ms` |

Los eventos posteriores a la finalizacion de agentes usan `context.Background()` con un timeout de 5 segundos para garantizar la entrega incluso si el plazo del contexto padre ha expirado.

## Configuracion

| Clave | Predeterminado | Descripcion |
|-------|---------------|-------------|
| `skills.orchestrate.enabled` | true | Habilitar/deshabilitar |
| `skills.orchestrate.max_tokens` | 0 (ilimitado) | Presupuesto compartido de tokens |
| `skills.orchestrate.max_agents` | 5 | Agentes paralelos maximos |
| `skills.orchestrate.timeout` | 60s | Timeout por agente |
| `skills.orchestrate.request_timeout` | 1h | Plazo general de orquestacion (maximo 4h) |

Todos los valores soportan recarga en caliente via `ConfigReloadable`.

## Frontend

El componente `AgentProgress.vue` muestra el estado de los agentes en tiempo real en la vista de chat, con iconos por tipo, nombre, estado y progreso.
