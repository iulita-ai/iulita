---
name: geolocation
description: Determine the user's public IP address and geographic location (country, city, timezone, ISP)
capabilities: []
config_keys:
  - skills.geolocation.api_key
secret_keys:
  - skills.geolocation.api_key
force_triggers:
  - geolocation
  - my location
  - where am i
  - my ip
  - ip address
  - local ip
  - public ip
  - local ip address
  - public ip address
  - what is my ip
  - what's my ip
  - show my ip
  - ip info
  - ip lookup
  - ip geolocation
---

# My Location / Geolocation

Display name: **my location** / **geolocation**

Use this tool to determine the user's current public IP address and geographic location.

## When to use
- User asks "where am I", "what's my location", "what country am I in"
- User asks for their public IP address
- User asks about their ISP or network provider
- User wants to look up geographic information for a specific IP address

## Input
- **ip** (optional string): specific IP address to look up. If omitted, auto-detects the user's current public IP.

## Output fields
- **IP** — public IP address
- **Country** — country name and ISO 3166-1 alpha-2 code
- **Region** — state, province, or region name
- **City** — city name
- **Timezone** — IANA timezone (e.g. Europe/Berlin)
- **ISP** — Internet Service Provider or organization name

## Formatting guidelines
- Present results in a clean, readable list
- Always show IP, country, city, and timezone
- If ISP is available, include it
- If the user seems to want persistent location context, offer to save it to memory
