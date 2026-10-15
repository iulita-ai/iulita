#!/usr/bin/env python3
"""Check OAuth JSON schemas and privilege-boundary examples, not an AS runtime."""
import copy
import json
from pathlib import Path

from jsonschema import Draft202012Validator, FormatChecker
from referencing import Registry, Resource
from referencing.jsonschema import DRAFT202012

ROOT = Path(__file__).resolve().parents[1]
spec = json.loads((ROOT / "api/mobile-oauth.openapi.json").read_text())
base = "urn:iulita:mobile:oauth:v1"
registry = Registry().with_resource(base, Resource.from_contents(spec, default_specification=DRAFT202012))
schemas = spec["components"]["schemas"]
for schema in schemas.values():
    Draft202012Validator.check_schema(schema)


def validator(name):
    return Draft202012Validator({"$ref": base + "#/components/schemas/" + name}, registry=registry, format_checker=FormatChecker())


checked = 0


def check(name, value, valid):
    global checked
    errors = list(validator(name).iter_errors(value))
    if bool(errors) == valid:
        raise AssertionError("Unexpected schema outcome for " + name)
    checked += 1


uid = "00000000-0000-4000-8000-000000000001"
challenge = {"issuer": "https://iulita.example", "owner_id": uid, "client_id": "iulita-android", "device_id": uid,
             "family_id": uid, "operation_id": uid, "key_thumbprint": "a" * 43, "purpose": "activate_device",
             "expires_at": 1792015600, "nonce": "b" * 43, "signing_input": "YWJj"}
active = {"access_token": "synthetic-access", "token_type": "Bearer", "expires_in": 900,
          "refresh_token": "synthetic-refresh", "scope": "chats:read chats:write", "iulita_token_stage": "active"}
pending = dict(active, expires_in=600, scope="", iulita_token_stage="pending_activation", iulita_challenge=challenge)
recovery = copy.deepcopy(pending)
recovery.update(expires_in=300, iulita_token_stage="provisional_recovery")
recovery["iulita_challenge"]["purpose"] = "recover_device"
for item in (active, pending, recovery):
    check("TokenResponse", item, True)
for invalid in (
    dict(active, iulita_token_stage="unknown"),
    dict(active, iulita_challenge=challenge),
    dict(pending, scope="devices:manage"),
    dict(pending, expires_in=601),
    dict(recovery, expires_in=301),
    dict(pending, iulita_challenge={**challenge, "purpose": "recover_device"}),
    {key: value for key, value in pending.items() if key != "iulita_challenge"},
):
    check("TokenResponse", invalid, False)

jwk = {"kty": "EC", "crv": "P-256", "x": "c" * 43, "y": "d" * 43}
check("InstallationJWK", jwk, True)
check("InstallationJWK", dict(jwk, d="private-key-material"), False)
check("InstallationJWK", dict(jwk, crv="P-384"), False)
check("InstallationJWK", dict(jwk, x="short"), False)
signing = {"kty": "RSA", "alg": "RS256", "use": "sig", "kid": uid, "n": "a" * 512, "e": "AQAB"}
check("SigningJWKS", {"keys": [signing]}, True)
check("SigningJWKS", {"keys": [dict(signing, d="private")]}, False)
check("SigningJWKS", {"keys": [dict(signing, alg="HS256")]}, False)
metadata = {"issuer": "https://iulita.example", "authorization_endpoint": "https://iulita.example/oauth2/authorize",
            "token_endpoint": "https://iulita.example/oauth2/token", "revocation_endpoint": "https://iulita.example/oauth2/revoke",
            "jwks_uri": "https://iulita.example/oauth2/jwks", "response_types_supported": ["code"],
            "grant_types_supported": ["authorization_code", "refresh_token"], "token_endpoint_auth_methods_supported": ["none"],
            "code_challenge_methods_supported": ["S256"], "scopes_supported": ["chats:read"],
            "iulita_mobile_protocol_version": 1, "authorization_response_iss_parameter_supported": True}
check("Metadata", metadata, True)
check("Metadata", dict(metadata, authorization_response_iss_parameter_supported=False), False)
refresh = {"grant_type": "refresh_token", "client_id": "iulita-android", "refresh_token": "synthetic"}
check("TokenForm", refresh, True)
check("TokenForm", dict(refresh, client_secret="synthetic"), False)  # gitleaks:allow -- negative-test fixture
check("TokenForm", dict(refresh, grant_type="password", username="test", password="synthetic"), False)  # gitleaks:allow -- negative-test fixture
code = {"grant_type": "authorization_code", "client_id": "iulita-android", "code": "synthetic", "redirect_uri": "https://iulita.example/mobile/oauth/callback", "code_verifier": "e" * 43, "iulita_installation_jwk": json.dumps(jwk)}
check("TokenForm", code, True)
check("TokenForm", {key: value for key, value in code.items() if key != "code_verifier"}, False)
check("TokenForm", dict(code, redirect_uri="http://iulita.example/mobile/oauth/callback"), False)

parameters = spec["paths"]["/oauth2/authorize"]["get"]["parameters"]
query_schema = {"type": "object", "properties": {p["name"]: p["schema"] for p in parameters}, "required": [p["name"] for p in parameters if p["required"]], "additionalProperties": False}
query_validator = Draft202012Validator(query_schema, format_checker=FormatChecker())
query = {"response_type": "code", "client_id": "iulita-android", "redirect_uri": code["redirect_uri"], "scope": "chats:read", "state": "random-client-state", "code_challenge": "f" * 43, "code_challenge_method": "S256", "iulita_installation_key_thumbprint": "g" * 43}
assert query_validator.is_valid(query)
assert not query_validator.is_valid(dict(query, code_challenge_method="plain"))
assert not query_validator.is_valid(dict(query, response_type="token"))
assert not query_validator.is_valid({k: v for k, v in query.items() if k != "state"})
assert not query_validator.is_valid(dict(query, state="short"))
assert not query_validator.is_valid(dict(query, state="bad-state-with-newline\n"))
assert spec["x-iulita-access-jwt-header"] == {"alg": "RS256", "typ": "at+jwt"}
print(str(len(schemas)) + " schemas and " + str(checked + 7) + " positive/negative contract cases passed")
