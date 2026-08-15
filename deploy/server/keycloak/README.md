# Keycloak — credencial forte da soberania de leitura

O que este realm existe para fazer: sob `AOS_MODE=production` o nó **recusa** derivar a fronteira
de soberania de um cabeçalho. O `board` passa a vir das *claims* de um ID-token verificado contra
este IdP, e o principal do leitor vem do `sub` do mesmo payload assinado. Um `X-Aos-Board` forjado
deixa de valer — é o ponto todo.

## Diferenças face ao realm de desenvolvimento

[`../../node/dev-hardened/keycloak/realm-aos.json`](../../node/dev-hardened/keycloak/realm-aos.json)
serviu de molde, mas duas coisas **não** foram copiadas:

**1. O `board` vinha fixo.** O realm de dev usa `oidc-hardcoded-claim-mapper` com
`board:demo` — todos os leitores desse cliente partilham a mesma fronteira, o que anula a
soberania por-leitor que o mecanismo existe para impor. Aqui é
`oidc-usermodel-attribute-mapper` sobre o atributo `board` do utilizador: **cada leitor traz o
seu**.

**2. Não há utilizadores neste ficheiro.** O realm de dev traz `alice` com a password `alice`.
Um realm versionado em git não é sítio para material de autenticação, e a identidade de leitura é
sua, não do repositório. `"users": []` é deliberado.

## Criar a sua identidade de leitura

Sem pelo menos um utilizador com o atributo `board`, **ninguém lê runs** depois de o modo produção
estar ligado. É o passo que tem de ser seu — envolve escolher uma password, e isso não é trabalho
que se delegue.

1. Entrar na consola de administração (as credenciais de bootstrap foram geradas no servidor —
   ver `/opt/aos/secrets/keycloak-admin.env`, root-owned).
2. Realm `aos` → *Users* → *Add user*. Definir o username.
3. Separador *Credentials* → definir a password (a política exige 14 caracteres, maiúscula,
   minúscula, dígito e símbolo).
4. Separador *Attributes* → acrescentar a chave **`board`** com o valor **`board:prod`**.

O valor tem de constar de `AOS_BOARD_REGIONS` no nó (hoje `board:prod=eu-west`). Um board que não
resolva para região faz a leitura ser **negada fail-closed** — não é um aviso, é um 403.

O passo 4 é o que se esquece. Sem o atributo, o token verifica e mesmo assim a leitura é recusada
com `id-token verificado sem claim 'board'` — que é o comportamento correcto e parece um bug.

### Porque é que `board` aparece no formulário

Não aparecia. O Keycloak 24+ traz o *user profile* declarativo ligado, e o realm só declara
`username`, `email`, `firstName` e `lastName`: qualquer outro atributo é **descartado em
silêncio**. O `POST` de criação devolve `201`, o atributo não fica guardado, e depois toda a
leitura é negada — um sintoma que aponta para o nó quando a causa está no IdP.

`provision-identity.sh` declara-o (passo 4b), com duas consequências que valem por si:

- **Validação por padrão** `^board:[a-z0-9][a-z0-9-]*$` — um board malformado é recusado com
  `HTTP 400` pelo próprio IdP, não descoberto mais tarde num 403 do nó.
- **`required`** — não se cria um leitor sem fronteira nenhuma.

Fica no script e não neste `realm-aos.json` de propósito: a importação do realm é **saltada**
quando ele já existe (`Strategy: IGNORE_EXISTING`, verificado), pelo que um realm provisionado
antes desta correcção nunca a receberia. No script é idempotente e alcança os dois casos.

## Obter um token

```bash
curl -s --cacert deploy/server/secrets-local/internal-ca/ca.crt \
  -X POST https://aos.elysiumii.site:9443/realms/aos/protocol/openid-connect/token \
  -d grant_type=password -d client_id=aos-node \
  -d username=<o-seu-utilizador> --data-urlencode "password=$PW" \
  | python -c 'import sys,json; print(json.load(sys.stdin)["id_token"])'
```

É o **`id_token`** que o nó quer, não o `access_token`. Apresenta-se como `Authorization: Bearer`
nas leituras (`GET /runs/{id}`, trajectória, DSAR).

O certificado do IdP é assinado pela **CA interna**, não por uma CA pública — daí o `--cacert`. A
CA privada vive só na máquina do operador, em `secrets-local/internal-ca/` (git-ignored): quem a
detivesse podia forjar um certificado para `idp` e personificar o IdP perante o nó, e por isso ela
está no mesmo sítio que a `issuer.key`, não no servidor.

## O que o token tem de trazer

| Claim | Origem | Se faltar |
|---|---|---|
| `sub` | Keycloak (UUID estável) | não há leitor resolvível ⇒ 403 |
| `board` | atributo do utilizador | `ErrNoBoardClaim` ⇒ 403 |
| `aud` | mapper `aud-aos-node` | audience não bate ⇒ recusa |

O nó impõe ainda **anti-replay**: idade máxima do token e recusa de reutilização de `jti`. Um
token capturado não vale duas vezes.

## Uma nota sobre o `iss`

O `KC_HOSTNAME` fixa o que o Keycloak assere em `iss`: `https://aos.elysiumii.site:9443/realms/aos`.
O nó, porém, **não sai à internet para o alcançar** — busca o JWKS por `https://idp:8443/...`, o
nome de rede interno, via `AOS_SOVEREIGN_OIDC_JWKS_URI`. É o que resolve o horizonte dividido: o
`iss` que o cliente vê e o caminho que o nó usa são deliberadamente diferentes, e o certificado do
IdP tem SAN para ambos os nomes.
