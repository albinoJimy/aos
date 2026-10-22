# Desenho R1b — o armazém de artefactos do run (ADR-043 §2.3), 2026-10-22

> Fase R1b do roteiro do ADR-043. Decisões do dono de 2026-10-22: (1) gravar no armazém do
> **próprio run** depois de o contexto ficar untrusted é **escrita local permitida** (emenda ao
> ADR-034); (2) a capability nova `cap:artifact.write` entra na política Cedar **assinada pelo
> dono**. Base: dois levantamentos do código (o armazém e a cadeia de uma capability nova).

## 1. O que o dono ganha

O modelo do run grava ficheiros — JSON, SVG, Markdown, CSV, texto — e o dono descarrega-os por
um link soberano. No exemplo: vê a planta (R1a) e grava a lista dos cômodos e uma planta em SVG.

## 2. As peças (AOS-548)

| Peça | Decisão |
|---|---|
| Armazém | `AOS_ARTIFACT_DIR` (vazio ⇒ desligado). Um ficheiro por artefacto em `<dir>/<sha256(run)>/<sha256 do conteúdo>`, cifrado com o `contentSealer` do nó (DEK por **titular**, a hierarquia das capturas — o ADR-043 dizia «por board»; corrige-se: é a do titular que a DSAR e a retenção cobrem). Sem deduplicação entre runs. Ligado sem o cifrador ⇒ o nó não arranca (nunca bytes em claro) |
| `artifact_write` | Código do nó, como a `media_view`. Entrada `{name, media_type, content}`; tipos fechados `application/json` (tem de ser JSON), `text/markdown`, `text/csv`, `text/plain`, `image/svg+xml` (XML bem formado; **recusa** `script`, `foreignObject`, atributos `on*`, `href` que não seja `#…`, entidades/DOCTYPE). Tectos: 1 MiB por artefacto, 16 artefactos e 16 MiB por run, e o tecto do volume (`AOS_ARTIFACT_MAX_BYTES`). Devolve uma referência pequena `aos-artifact-ref/v1` (digest, tipo, bytes, nome) |
| Run e passo | Um rewriter como o da `ocr_read` põe run, passo e titular na entrada: a tool sela a proveniência sem os pedir ao modelo |
| Proveniência | Evento `artifact.created` no stream do run (família nova `artifact.*` em tecnica/13), produtor `nhi:aos-node/artifact`, `StepID` com prefixo próprio; leva o digest, o tipo, o tamanho, o nome, a tool, o passo e o `sealed_subject` (a retenção e a DSAR passam a contá-lo — `subjectOf`) |
| Leitura | `GET /runs/{id}/artifacts` (lista, dos eventos) e `GET /runs/{id}/artifacts/{digest}` pelas portas de `portasDaLeituraDeConteudo` (D7, posse do stream, selo D6), capability `read:artifact`; serve só um digest que o **evento do próprio run** declara; `Content-Disposition: attachment`, `nosniff`, `Content-Security-Policy: sandbox; default-src 'none'`, `Cache-Control: no-store`; apagado ⇒ 410 |
| Replay | O resultado da tool (a referência) é capturado; o replay não volta a gravar |

## 3. A escrita local do run (AOS-549)

Um eixo declarado novo no registo de tools, `mutation: "run_artifact"` — «escreve só no armazém
do próprio run, sem egress, apagável» —, que cada classificador lê da mesma maneira:

| Onde | Hoje | Com `run_artifact` |
|---|---|---|
| Registo do nó (`validateMutation`) | `none` ou `mutates` | aceita `run_artifact` **só** com `cap:artifact.write`, `egress: none`, `reversible` e uma tool de código do nó |
| Gate de taint (`ClassifyCall`) | `mutates` ⇒ privilegiada | `run_artifact` ⇒ não privilegiada, razão `local_run_write` |
| Risco (`localCapabilityPrefixes`) | `cap:artifact.*` ⇒ egress desconhecido ⇒ `danger` | `cap:artifact.` local ⇒ `gray`; em L4 corre |
| Política Cedar | sem permit ⇒ negada | `allow_artifact_write` com `context.taint != "untrusted" || context.destination == "run_artifact"`: a excepção fica na política **assinada**, e o `destination` vem do registo trusted, nunca do modelo |
| Planeador (`IsEffectTool`, `deriveNodeAction`) | `mutates` ⇒ efeito irreversível ⇒ cartão humano | `run_artifact` ⇒ efeito local reversível; não é ferramenta de verificador |

Emendas: ADR-034 (a escrita local do run, decisão do dono) e ADR-022 (o eixo de mutação do plano).

## 4. O que é do dono

1. **Assinar o bundle de política 1.1.0** (`go run ./cmd/policy-sign -version 1.1.0`, com a chave
   da política). Atenção: sem a chave em `~/.aos/keys/signing.key` a ferramenta gera um par novo —
   rotação da âncora (`AOS_POLICY_TRUST_ANCHOR` no `.env`).
2. **Re-assinar o mandato** da cunhagem sem operador com `cap:artifact.write`.
3. **Autonomia do domínio `artifact`**: `AOS_AUTONOMY_LEVELS` e as duas provas de subida a L4.
4. **`secrets/authority.json`**: `cap:artifact.write` em `agent:agent-worker` e no humano, com a
   revisão incrementada.

## 5. Ordem

AOS-548 (desligado, provado com testes e smoke) → AOS-549 (código + bundle por assinar) →
cerimónias do dono → deploy → prova: um plano vê a planta e grava `comodos.json` e
`planta.svg`; o dono descarrega os dois.
