---
name: weather
description: Current weather and forecasts for any location
capabilities: []
config_keys:
  - skills.weather.openweathermap_api_key
secret_keys:
  - skills.weather.openweathermap_api_key
force_triggers:
  - weather
  - forecast
  - погода
  - прогноз
  - температура
---

# Weather Forecast

Display name: **weather** / **погода**

Use this tool to get weather information for any location.

## When to use
- User asks about weather, temperature, forecast, rain, snow
- User asks "what's the weather like", "будет ли дождь"
- User wants to know if they need an umbrella, jacket, etc.

## Input
- **location** (optional string): known city or address, or the exact `"lat, lon"` coordinates the user shared in this conversation (e.g., a Telegram map pin). If no city is known, leave empty: the application defaults to **Иркутск (Irkutsk, Russia)** without automatic IP geolocation.
- **days** (optional integer): number of forecast days (1-16). Default is 1 (today only). Use 2 for today+tomorrow, 7 for a week.

## Important
- Explicit user instructions and known preferences take precedence over the fallback. Never guess another city from language, server IP or earlier assistant guesses.
- The fallback is not a detected location or a change to the user's profile. Name the city in the forecast so the user can correct it.
- EXCEPTION: coordinates the user shared in this message OR in a recent previous message (a "[Location]: lat, lon" or "[Place]: … (lat, lon)" marker in the conversation) are not memory context — pass the coordinate string as the location parameter directly.
- Never call weather twice in one turn. One call is enough.

## Output fields
- **Location** — resolved location name
- **Date** — forecast date
- **Description** — weather conditions (e.g., "Partly cloudy", "Heavy rain")
- **Temperature** — min/max in Celsius
- **Precipitation** — total in mm
- **Wind** — max speed in km/h
- **UV Index** — when available

## Formatting
- For single-day: compact inline format
- For multi-day: markdown table (when channel supports markdown)
- Always include temperature range and weather description
