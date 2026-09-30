# Microsoft Entra credentials

Agent_b can sign a connector in as the person using this Windows account. Sign-in stays in Settings; a chat or tool can use an existing sign-in but cannot start or complete one.

## Register the public client

Create an app registration in the tenant that owns the target API:

1. Add a **Mobile and desktop applications** platform with the loopback redirect URI `http://localhost`. Agent_b uses an available loopback port for authorization code with PKCE.
2. Treat it as a public client. Do not create or enter a client secret.
3. Add the target API's delegated permissions. Enter those scope values in Agent_b exactly as the API publishes them.
4. If operators may choose device-code sign-in, enable public client flows for the registration. Browser sign-in does not require enabling device code.
5. Grant or consent to those delegated permissions according to the tenant's policy.

The target API must validate the token issuer and audience. Its exposed Application ID URI (or other documented audience) must match the audience the API validates. Authorize the signed-in person with the API's own policy, such as an assigned app role or an allowed group; receiving a valid token is not by itself authorization to every operation.

## Add and sign in

In **Settings → Security → Credentials**:

1. Open **Add Microsoft Entra**.
2. Enter a credential name such as `<credential-name>`.
3. Enter the target API origin, for example `https://<api-host>:<port>`. Tokens are attached only to this exact HTTPS scheme, host, and port.
4. Enter `<tenant-id>`, `<public-client-id>`, and the space-separated delegated scopes, such as `<api-scope-read> <api-scope-write>`. Agent_b supplies no defaults.
5. Press **Add Entra credential**.
6. Press **Sign in with browser**. To choose the fallback explicitly, press **Use a device code**, open the shown verification address, and enter the shown code.

Settings shows the signed-in account. **Switch account** replaces that credential's cached account; **Sign out** clears its protected token cache. If consent is revoked or refresh expires, the next connector call says to sign in in Settings and does not retry in a loop.

## Use it from a connector

Set the connector auth reference to `entra:<credential-name>`. The connector's `base_url` origin must equal the origin stored with the credential.

Placeholder example:

```json
{
  "kind": "http",
  "base_url": "https://<api-host>:<port>",
  "auth": "entra:<credential-name>",
  "allowed_methods": ["GET", "POST"]
}
```

An imported OpenAPI document describes operations and parameters, but it never supplies identity configuration. The connector keeps the `entra:` reference, and the API still decides what the signed-in account may do.

## Storage and sign-out

The MSAL refresh/account cache is persisted with the same Windows user-scope DPAPI protection as other Agent_b credentials. Access-token entries are removed before that cache is written and remain in process memory. User-scope DPAPI protects against other Windows users; it does not isolate secrets from arbitrary programs already running as the same user. The separate service-account OS boundary remains the protection against a tool process.
