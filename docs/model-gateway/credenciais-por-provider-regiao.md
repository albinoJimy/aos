# Configuração de credenciais por provider/região (AOS-056)

Este documento descreve como configurar a aquisição de credenciais de infra do
Model Gateway (GW) por provedor e região. Cobre o Credential Broker/Vault, a
camada OAuth por provedor e o cache JIT. Referências: `tecnica/06 §4.1`,
`tecnica/07 §7.1`, ADR-006.

> Invariante fundadora (ADR-006): a chave de infra **nunca** chega ao agente nem
> a logs/spans; a aquisição corre **server-side**; sem credencial válida o GW
> falha *fail-closed* com erro atribuível (provider+região), nunca caindo para
> outra conta/região.

## 1. Componentes

- **`CredentialBroker` (`internal/credentials/broker.go`)** — porta do broker/vault.
  `Issue(provider, região) -> Lease{LeaseID, ExpiresAt, secret}` e `Revoke(leaseID)`.
  - `FakeBroker` — determinista, para testes e ambientes locais.
  - `ReferenceBroker` — stub que documenta o vault real e falha *fail-closed*
    (`ErrNotWired`) até ser ligado por infra (EPIC-07).
- **Camada OAuth (`internal/adapters/oauth/`)** — traduz o mecanismo de cada
  provedor para a credencial de infra (registo `oauth.Registry`).
- **`Source` (`internal/credentials/source.go`)** — implementa
  `adapters.CredentialSource`; cache JIT com TTL, *refresh* antes de expirar,
  rotação sem interromper *in-flight* e revogação.

## 2. Mecanismos OAuth por provedor

| Provedor (`provider`) | Mecanismo | Notas de configuração |
|---|---|---|
| `openai` | `api_key` | *Pass-through*: a API key do vault é o portador `Bearer`. TTL = TTL do lease. |
| `anthropic` | `service_oauth` | OAuth de serviço (*client-credentials*). Requer `TokenTTL` curto. |
| `google` | `federated` | Identidade federada; a **região** entra na derivação (audiência regional). |

O registo dos três provedores faz-se com `oauth.DefaultRegistry(oauth.Options{...})`
ou individualmente (`oauth.NewOpenAI`, `oauth.NewAnthropic`, `oauth.NewGoogle`).

## 3. Configuração da `Source`

```go
reg := oauth.DefaultRegistry(oauth.Options{TokenTTL: 5 * time.Minute})
src := credentials.NewSource(broker, reg, credentials.Config{
    TTL:         10 * time.Minute, // vida da credencial em cache (curta)
    RefreshLead: 1 * time.Minute,  // renova antes de expirar
    Allowed: []credentials.ProviderRegion{
        {Provider: "openai", Region: "eu"},
        {Provider: "anthropic", Region: "eu"},
        {Provider: "google", Region: "us"},
    },
    Clock: clock, // injectável (determinismo)
})
gw := modelgateway.New(adapter, modelgateway.WithCredentialSource(src), ...)
```

### Campos

| Campo | Significado | Default |
|---|---|---|
| `TTL` | Vida curta da credencial no cache. | 10 min |
| `RefreshLead` | Antecedência de renovação (`now >= ExpiresAt − RefreshLead`). | 1 min |
| `Allowed` | Pares `(provider, região)` elegíveis. **Vazio = permissivo** (usar só quando a soberania é imposta a montante). | — |
| `Clock` | Relógio injectável para TTL/rotação. | `time.Now` |

## 4. Fronteira de soberania e *fail-closed*

- A chave servida corresponde **sempre** ao par `(provider, região)` **exacto**
  pedido. A *allowlist* regional concreta é AOS-058; aqui garante-se a config e a
  selecção correcta.
- Um par fora de `Allowed` → `*CredentialError` com `ErrRegionNotConfigured`
  (atribuível). O broker nem é consultado.
- Par configurado mas sem material no broker → `*CredentialError` com
  `ErrNoMaterial` (atribuível), **nunca** a chave de outra região.

## 5. Rotação e revogação (operação)

- **Rotação:** o vault roda a chave (no fake, `FakeBroker.SetSecret`). A próxima
  renovação (dentro da janela de *refresh*) passa a servir a chave nova; as
  chamadas em curso completam com a chave antiga (sem corte).
- **Revogação:** `Source.Revoke(ctx, provider, região)` invalida o cache e revoga
  o lease no broker. A próxima `Fetch` obtém uma credencial nova ou falha
  *fail-closed* se o material foi removido.

## 6. Ligação ao vault real (infra/EPIC-07)

Substituir o `FakeBroker` por uma implementação de `CredentialBroker` que fale com
o vault real (HashiCorp Vault / cloud KMS + broker server-side). O contrato é
apenas `Issue`/`Revoke`; o resto do GW (OAuth, cache, GW) não muda. O
`ReferenceBroker` fixa a forma e recusa emitir até estar ligado — impede uso
acidental sem vault.
