---
name: share_location
description: Send a place to the user as a native map pin or venue card in the CURRENT chat (native on Telegram, a map link on other channels). Use when the user asks to share, send, show, or pin a place or coordinates on the map, or to forward a location. This tool only SENDS a place you already know — it never detects the user's position (for IP-based "where am I", use geolocation) and it cannot look up place names itself; if you only know a place name, resolve it to coordinates first or state the address.
---

# Sharing places on the map

The `share_location` tool sends one place per call into the chat the user is
currently in — it cannot target other chats.

- The native pin/venue card is sent while the tool runs, so it appears ABOVE
  your reply. Never write "card incoming below" or re-describe the map; if you
  refer to it, say the pin was sent above.
- Give the place's name and useful context in your text, but do NOT repeat the
  full address verbatim — the card already shows it.
- Prefer a venue card (title + address) for named places; a bare pin when only
  coordinates are known. Never invent coordinates: if unsure where a place is,
  geocode it first or say so.
- On channels without native map support the tool returns a map link — include
  it in your reply verbatim, as a plain URL on its own line. Do NOT wrap it in
  a markdown link: some terminals drop link URLs.
- Never send back a location the user just shared unless they explicitly ask
  you to share or forward it.
- Limits: at most 10 sends per chat per hour; on refusal, relay the returned
  map link instead and tell the user the limit was hit.
