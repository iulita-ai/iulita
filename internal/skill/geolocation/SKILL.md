---
name: geolocation
description: Determine the user's public IP address and geographic location (country, city, timezone, ISP), or resolve a place name to coordinates (geocode)
capabilities: []
config_keys:
  - skills.geolocation.api_key
  - skills.geolocation.geocode_enabled
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

Use this tool to determine the user's current public IP address and geographic location, or to resolve a place name to coordinates.

## When to use
- User asks "where am I", "what's my location", "what country am I in"
- User asks for their public IP address
- User asks about their ISP or network provider
- User wants to look up geographic information for a specific IP address
- You need coordinates for a place name the user asked to share on the map (feeds `share_location`)

## When NOT to use
- NOT for IP-based lookups when the user shared a map pin or venue in this or a recent message (a "[Location]: lat, lon" or "[Place]:" marker in the conversation) — answer from those shared coordinates instead: the IP lookup reflects the server network, not the position the user shared. The action=geocode path remains available for resolving OTHER places the user asks about.

## Input
- **ip** (optional string): specific IP address to look up. If omitted, auto-detects the user's current public IP.
- **action** (optional string): set to `"geocode"` to look up coordinates for a place name instead of an IP.
- **query** (string, required when action=geocode): place name or address to geocode.

## Output fields (IP lookup)
- **IP** — public IP address
- **Country** — country name and ISO 3166-1 alpha-2 code
- **Region** — state, province, or region name
- **City** — city name
- **Timezone** — IANA timezone (e.g. Europe/Berlin)
- **ISP** — Internet Service Provider or organization name

## Output (geocode action)
One line: `Display Name — lat, lon (data © OpenStreetMap contributors)`. Pass the
coordinates to `share_location` when the user asked for the place on the map.
When calling `share_location`, pass the first display-name segment as `title`
and the remainder as `address` so a venue card (not a bare pin) is sent.
Keep the OSM attribution when quoting the result verbatim.

## Formatting guidelines
- Present results in a clean, readable list
- Always show IP, country, city, and timezone
- If ISP is available, include it
- If the user seems to want persistent location context, offer to save it to memory
