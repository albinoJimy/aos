# Análise crítica: o harness está a ficar estreito? (2026-10-22, v2 depois da validação adversarial)

> **Pedido do dono.** Reavaliar a arquitectura contra a visão inicial: um harness flexível,
> extensível e agnóstico de modelos, que use a OpenRouter para escolher e combinar modelos
> conforme a tarefa e que produza qualquer tipo de artefacto.
>
> **Base.** Dois levantamentos do código, só de leitura, e duas revisões adversariais
> independentes: uma aos **factos**, outra à **proposta**. A v1 deste documento tinha erros. A
> §8 diz o que caiu e porquê.

## 1. Veredicto

A sensação do dono está certa **no diagnóstico de fundo**:

- cada capacidade nova entrou como um **caso especial**: um nome de tool fixo em Go, um campo
  novo na porta, uma constante nova, um ADR novo;
- o custo de acrescentar a próxima capacidade cresce de forma linear;
- o desenho da A5/A6 que apresentei hoje (uma tool por modalidade) repetia esse padrão.

Mas a validação mostrou duas coisas que a v1 escondia:

1. **Quase todas as restrições são decisões registadas, não acidentes.** Cada uma tem um
   motivo escrito num ADR, na Carta ou num deferimento. Revertê-las é legítimo, mas obriga a
   **responder à objecção original** de cada uma.
2. **A parte mais ambiciosa da v1 era insegura.** A v1 propunha que chamar modelos com
   conteúdo do run «não é exfiltração». Isso é **falso**, pelas razões da §5. Além disso, mexe
   no **ADR-013, que a Carta marca como FIXO**: exige emenda da Carta, não só um ADR.

**A direcção que se aguenta:** partes de conteúdo genéricas, um armazém de artefactos,
modelos escolhidos pela tarefa a partir de um catálogo **qualificado** e tools declarativas.
Tudo isto **dentro** das fundações (RM, NHI, execução durável, SA-ROC), e não por excepção a
elas.

## 2. Onde está a rigidez, com a razão de cada uma

| Restrição hoje | Razão registada | Veredicto da validação | O que custa ao dono |
|---|---|---|---|
| Um modelo do run por nó, mais no máximo 1 candidato (`modelo_candidato.go:44`) | ADR-041, decisão D6 | Confirmada; é decisão | Não se escolhe o modelo pela tarefa. O planeador usa o mesmo modelo só porque o compose lhe passa a mesma variável: separá-los é **uma linha de env** |
| Um modelo novo exige: banco de ensaio, 2 assinaturas (allowlist + perfil), cerca de 6 ficheiros. **Não exige release** (allowlist, preços e perfis vêm de pastas externas) | AOS-519/522; PROC-BANCO-DE-ENSAIO | A v1 dizia «1 release»: **falso** | O custo real é **qualificar e assinar cada modelo**, desenhado para menos de 1 h |
| O nó não conhece o catálogo da OpenRouter (sem `/models`) | — | Confirmada | Não descobre que modelos geram imagem, áudio ou vídeo |
| O router (scoring ADR-021) está **composto no gateway**, mas é **inerte no nó**: não há escada de tiers declarada e o task-fit vale 0 | DEF-280-NO (AOS-421), DEF-280-PORTAS | A v1 dizia «desligado»: **parcial** | Ligá-lo **não basta**: falta o critério «serve para esta tarefa» |
| A resposta do modelo é só texto (`port.go:182`) | AOS-509: recusar em vez de perder conteúdo | Confirmada; é decisão | Não recebe imagens nem áudio gerados |
| O preço só se conta por token (`pricing/table.go:83`) | — | Confirmada | Gerar imagem ou vídeo não se contabiliza |
| 3 tools em produção, todas de leitura | ADR-034 §2.7: o `web_post` foi retirado por decisão do dono | Confirmada; é decisão | O agente lê mas não age |
| Só `ocr_read`/`video_read` chamam um modelo; o **efeito** está em Go (o alias já é configuração) | ADR-042 §2.1; nunca se avaliou uma tool genérica (§3) | Confirmada | Cada **uso** novo de um modelo exige código e ADR |
| O guest gVisor só sabe `read` | — | Confirmada | Não há execução de código |
| O MCP está construído mas tem zero chamadores no nó | DEF-812 | Confirmada; deferida | Não há forma declarativa de ligar ferramentas externas |
| O Cedar tem só 2 `permit` | ADR-034 §2.6 (cláusula de taint adiada para a cerimónia de chave) | A v1 exagerava: a ocr e o vídeo **reutilizaram** `cap:fs.read` | O bundle **não** é o gargalo de cada tool nova |
| As tools de um turno correm em série (`loop.go:836`) | O step_id por índice e a escalada antes do checkpoint dependem dessa ordem | Confirmada | Paralelizar mexe no replay e na idempotência; não é trivial |
| Um run não lança sub-agentes | ADR-018: o multi-nó vive no `aos-orq` | Confirmada; é decisão | A decomposição só se faz no planeamento |
| Não há armazém de artefactos; `PayloadArtifact` é opaco | — | Confirmada | Nada do que se gera sobrevive |
| Depois de o run ler algo, uma tool privilegiada com egress externo é **negada, ou escalada para uma pessoa** (AOS-541) | ADR-034; destino declarado = **opção A** (CaMeL por handles) | A v1 dizia «sempre negada»: **parcial** | Gerar a partir do que se viu exige aprovação humana em cada chamada |
| O titular não vê media directamente | ADR-042 §3, adiado (gatilho D1: custo por turno, `prompt_hash`, replay) | — | A A5 original fica por fazer |

## 3. Pressupostos que se mantêm de pé

1. **Uma modalidade não deve ser um campo na porta.** Uma **parte de conteúdo genérica**
   (`media_type` + bytes ou referência ao armazém) cobre imagem, áudio, vídeo e PDF. A resposta
   «só texto» passa a «partes declaradas pela rota». A garantia do AOS-509 mantém-se: uma parte
   não declarada continua a ser recusada, não perdida.
2. **Usar um modelo como ferramenta não deve exigir uma tool em Go.** Uma tool genérica de
   invocação de modelo, configurada por entrada no `tools.json`, faz o trabalho. A `ocr_read` e
   a `video_read` passam a ser **configurações** dela.
3. **O modelo deve escolher-se pela tarefa, e não um por nó.** Mas os requisitos vêm do
   **plano ou do objectivo confiável**, nunca do conteúdo untrusted (§5.3).
4. **Um modelo novo não deve depender de trabalho à mão por modelo.** A política por atributos
   define a **elegibilidade**; a entrada no catálogo servível continua a exigir o **veredicto
   do banco** (ADR-041/042), agora automatizado e assinado **por lote**, não por modelo.
5. **Uma tool nova não deve exigir código.** Há três fontes declarativas: MCP (DEF-812),
   HTTP declarado e execução de código. **Cada servidor MCP e cada host HTTP é um destino na
   allowlist**, não uma simples configuração.

Pressupostos da v1 que **caíram**:

- **«A aprovação humana só para efeitos irreversíveis».** Contradiz o ADR-013: o egress
  externo de dados sensíveis é DANGER.
- **«Chamar o nosso gateway não é exfiltração».** Ver §5.

## 4. Arquitectura proposta (v2)

```
 Política assinada pelo dono ─ elegibilidade por atributos + orçamentos + fornecedores permitidos
        │
 Catálogo QUALIFICADO (snapshot da OpenRouter ∩ política ∩ veredicto do banco, assinado por lote)
        │
 Pedido → Planeador ── requisitos por passo (só de contexto confiável) ──► Router (ADR-021 + correspondência por modalidade)
                                                                                │  decisão selada (modelo, fornecedor efectivo, preço)
 Run (loop) ──► RM inalterado: identidade · política · SA-ROC (ADR-013) · taint · orçamento · egress
   ├─ model_invoke     (partes genéricas; entradas por handle/digest — opção A do ADR-034)
   ├─ MCP / HTTP       (cada servidor ou host = destino na allowlist)
   ├─ code_exec        (gVisor sem rede; a saída é untrusted e vai para o armazém)
   └─ artifact_*       (escrever, ler, converter)
        │
 Armazém de artefactos (digest; cifra por board; proveniência selada; o artefacto herda o taint; retenção/DSAR)
        │
 Entrega: nó seguinte (por referência) · cliente (GET soberano com attachment + CSP sandbox + SVG/HTML sanitizados)
```

## 5. O que a v1 subestimava (segurança)

1. **O subprocessador.** O argumento «o conteúdo já vai para o fornecedor» só vale para o
   fornecedor **do titular**. Cada modelo do catálogo pode ser outro fornecedor, com outra
   retenção. **Defesa:** a política lista os fornecedores permitidos e cada pedido fixa o
   fornecedor (`provider.order`, `allow_fallbacks=false`, `zdr`). O fornecedor efectivo fica
   selado.
2. **O artefacto como canal de fuga.** Uma injecção pode mandar esconder dados num SVG, HTML ou
   PDF (links, `<img src=atacante>`). Quem abre esse ficheiro faz o pedido ao atacante.
   **Defesa:** o artefacto é untrusted, a entrega é por `attachment` com CSP `sandbox`, e o
   SVG/HTML é sanitizado. O HTML fica fora até haver um caso.
3. **O conteúdo untrusted a escolher o modelo e o custo.** Isso é «comandar» (AGENTS §7,
   princípio 4). **Defesa:** os requisitos vêm do plano ou do objectivo confiável. Dentro do
   run, as entradas da `model_invoke` passam por **handle** (opção A do ADR-034), não por texto
   que o modelo copie.
4. **A troca de modelo atrás do nome.** A governação da rota **não detecta** uma troca feita
   pelo fornecedor (`route.go:18-30`). Desde o AOS-543 só se impõe o modelo, e um snapshot do
   `/models` não versiona pesos. **Defesa parcial:** fixar o fornecedor e a quantização por
   pedido. O resto é **resíduo declarado**.
5. **`code_exec` e MCP.** Não ter rede não chega. A saída volta ao contexto e aos artefactos
   (canal do ponto 2). Os schemas MCP são untrusted e os servidores de terceiros entram na
   cadeia de supply-chain (ADR-017).
6. **O ADR-013 é FIXO na Carta.** Uma classe de egress «gateway» que não seja DANGER muda o
   eixo de egress do SA-ROC. Isso é **emenda da Carta** (§6.5, com dois papéis a arbitrar) e
   conta para o tripwire de 30 dias. **A v2 evita-o:** a `model_invoke` continua a ser egress
   externo, classificado pela sensibilidade dos dados. Com dados públicos ou internos, segue.
   Com dados sensíveis, pede aprovação.

## 6. O que falta medir ou desenhar

- **OpenRouter** (documentação lida, não medida):
  - o `/api/v1/models` dá `input_modalities`/`output_modalities` e preços por `prompt`,
    `completion`, `request`, `image`, `image_output`, `audio`, …;
  - **não há preço por segundo de vídeo**: o vídeo é outra API, assíncrona
    (`/api/v1/videos`, com catálogo próprio);
  - **a retenção e o treino não vêm no objecto do modelo**: vêm por endpoint e por filtros
    (`zdr`);
  - os `overrides` de preço tornam a reserva não determinista.
- **O LiteLLM é um obstáculo real.** Só fala chat e não declara o endpoint. Um adaptador
  directo à OpenRouter é um **destino de egress novo** (o guard-test AOS-069 recusa-o) e exige
  decisão.
- **Omissões da v1:**
  - moderação do conteúdo gerado;
  - a geração não é reprodutível: o replay só funciona se o artefacto for **capturado** (com
    custo de disco, retenção e DSAR);
  - o custo de jobs assíncronos é pago mesmo que o run falhe;
  - quotas e rate limits por fornecedor;
  - suporte variável a saída estruturada;
  - spans OTel para `generate` e `job`;
  - onde a `model_invoke` cabe na escada de autonomia (ADR-014);
  - «por titular» deve ler-se **«por board»** (a v1 da Carta não é multi-tenant).
- **Cadência real de modelos novos (por mês)**: ainda não medida. É ela que justifica, ou
  não, a urgência do catálogo.

## 7. Roteiro reordenado: primeiro o caminho pequeno e seguro

| Fase | Conteúdo | Porquê nesta posição | Tamanho |
|---|---|---|---|
| R0 | ADR «harness por capacidades»: fixa a direcção, responde às objecções originais (D6, AOS-509, ADR-018, ADR-042 §3/D1, DEF-812) e posiciona-se como **concretização da opção A** do ADR-034. **Não** toca no ADR-013 | Direcção sem emenda da Carta | S |
| R1a | Parte de conteúdo genérica **só de entrada** + o titular vê media (A5), com o perfil `imagem` qualificado no banco e o replay pelo digest | O caminho mais pequeno para «foto → cômodos»: nenhum fornecedor novo nem classe de egress nova; o dado vai a quem já o recebe | M |
| R1b | Armazém de artefactos + `artifact_write`/`artifact_read` + GET soberano (attachment, CSP, sanitização) | O titular passa a produzir JSON/SVG/Markdown: **o caso fica entregue** (lista e planta dos cômodos) sem modelos de geração | M |
| R2 | Opção A do ADR-034: entradas por handle e artefacto com taint | Pré-condição para qualquer tool que envie conteúdo do run para fora; sem ela a R3 nasceria negada | L |
| R3 | `model_invoke` genérica + partes de **saída** + preço por unidade (imagem) + fornecedor fixado e selado; `ocr_read`/`video_read` passam a configuração | Gera imagens dos cômodos; a A6 fica fechada para imagem | L |
| R4 | Catálogo qualificado (snapshot + política + banco em lote) + router com correspondência por modalidade | Só compensa quando há vários consumidores | L |
| R5 | `code_exec` na gVisor + MCP ligado (cada servidor um destino) | Ferramentas sem código novo | L |
| R6 | Vídeo gerado (API assíncrona) | Só com caso | L |

Se o trabalho parar a meio, cada fase deixa algo útil e coerente. Nenhuma deixa uma peça sem
consumidor nem uma tool que nasce negada.

## 8. O que caiu na validação adversarial (v1 → v2)

| v1 dizia | Verdade | Fonte |
|---|---|---|
| «1 release por modelo» | 0 releases; o custo é qualificar e assinar | revisão aos factos |
| «Router desligado» | Composto no gateway, inerte no nó; falta o task-fit | idem (DEF-280-NO/PORTAS) |
| «Gerar depois de ver é sempre negado» | Negado ou escalado para uma pessoa (AOS-541) | idem |
| «Cedar é o gargalo de cada tool» | Não é: a ocr e o vídeo reutilizaram `cap:fs.read` | idem |
| «Chamar o nosso gateway não é exfiltração» | Falso: outro subprocessador, o artefacto como canal, o untrusted a escolher | revisão à proposta |
| «Aprovação só para efeitos irreversíveis» | Contradiz o ADR-013 (FIXO) | idem |
| «A comparação da rota defende contra a troca de modelo» | O código diz que não detecta a troca feita pelo fornecedor | idem (`route.go:18-30`) |
| «Política por atributos assinada uma vez» | Perde a qualificação medida; passa a elegibilidade + banco por lote | idem |
| A5 no fim (R6) | É o caminho mais pequeno e seguro; vai para o início | idem |
