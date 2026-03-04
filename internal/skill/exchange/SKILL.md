---
name: exchange
description: Currency exchange rate lookup
capabilities: []
config_keys: []
secret_keys: []
---

# Currency Exchange Rates

Use this tool to look up current exchange rates between currencies.

## When to use
- User asks about currency conversion or exchange rates
- User asks how much X in one currency is worth in another
- User mentions prices in foreign currencies and wants local equivalent
- User asks to compare currencies

## Input
- **from**: base currency code (e.g. USD, EUR, UAH, GBP). Required.
- **to**: target currency code or comma-separated list (e.g. "EUR" or "EUR,GBP,JPY"). Required.
- **amount**: amount to convert (default 1).

## Currency codes
Standard ISO 4217 three-letter codes: USD, EUR, GBP, JPY, CHF, CAD, AUD, CNY, INR, BRL, RUB, KRW, TRY, PLN, CZK, SEK, NOK, DKK, HUF, RON, BGN, HRK, ISK, MXN, ARS, CLP, COP, PEN, etc.

## Formatting
- Always show the amount, source currency, rate, and result
- For multiple target currencies, show each on a separate line
- Round results to 2 decimal places for most currencies; use 4 for rates < 0.01
