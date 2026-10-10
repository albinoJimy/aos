# Desenho A3 — entrada automática de um modelo: qualificar, assinar, servir, recuar

> **Estado deste documento.** Desenho apresentado ao dono a 2026-10-11, no dia em que fechou a
> fase A2 e abriu a A3. **Nenhum ticket está aberto e nenhum código foi escrito:** os tickets da
> A3 só se abrem depois de o dono tomar as decisões D1 a D9 (§6). Os itens de trabalho têm
> rótulos provisórios — «A3-arnês», «A3-perfil», e assim por diante — que não são identificadores
> de ticket; a numeração faz-se na altura, com `scripts/ci/sessoes.py reservar`.
>
> Data: 2026-10-11. Base lida: `feature/AOS-128-ux-dx-tests` @ `725df5e8`. Produção: v0.1.53.
> Trabalho de leitura e desenho: nada foi pedido a um modelo nem a produção.
>
> **Como ler as marcas.** `[CÓDIGO]` = lido no código, com ficheiro:linha. `[MEDIDO]` = número
> registado no documento de acompanhamento
> (`acompanhamento-arquitectura-alvo-fronteira-modelo.md`, §5). `[HIPÓTESE]` = não confirmado.
> `[NÃO ENCONTREI]` = procurei e não está lá.
>
> Abreviaturas de caminho: `gw/…` = `packages/platform/model-gateway/…`; `nó/…` =
> `packages/cmd/aos/…`; `orq/…` = `packages/cmd/aos-orq/…`; `banco/…` =
> `packages/qa/banco-ensaio/…`; `reg/…` = `packages/platform/registry/…`.

## 0. Resumo numa página

Hoje, pôr um modelo novo a servir runs custa **um PR e uma imagem nova do nó**: o perfil da rota
é uma tabela escrita em código, e o nó pede sempre o mesmo modelo, lido de uma variável de
ambiente. A qualificação existe — o banco de ensaio da A2 — mas o seu relatório é uma folha de
taxas que uma pessoa lê; não é um veredicto que uma máquina possa conferir.

A A3 troca isso por um caminho com quatro passos, e só um é do dono:

1. **Qualificar** — a máquina corre a bateria contra o modelo, com os controlos negativos, e
   escreve um veredicto com regra fixa.
2. **Assinar** — o dono assina o perfil, que cita o digest desse veredicto. É a única assinatura.
3. **Servir aos poucos** — o nó carrega o perfil assinado no arranque e dá ao modelo novo uma
   fatia pequena do tráfego.
4. **Recuar sozinho** — se o modelo novo falhar mais do que o limiar escrito, o nó deixa de lhe
   dar tráfego, sem ninguém intervir, e fica registado porquê.

O que torna isto mais pequeno do que parece: três das peças já existem em parte. O gateway já
aceita perfis que não estão na tabela (`ProductionConfig.RouteProfiles`), o nó já sabe carregar
uma política assinada de uma pasta e conferi-la contra uma chave fixa (a allowlist de modelos),
e o banco já calcula um veredicto com regra escrita para a devolução do estado. Falta juntá-las,
e falta o que não existe de todo: o nó escolher entre dois modelos, e o disjuntor.

A recomendação deste desenho é fazer **o mínimo que cumpre o critério da fase** — o terceiro
modelo entra com zero PRs e uma assinatura em menos de uma hora; um modelo mau é recusado
sozinho — e deixar para a A4 tudo o que é escolher o melhor modelo para cada passo.

## 1. O que a A3 promete e o que não promete

### Promete

- **Um modelo de uma classe já qualificada entra sem código.** «Classe já qualificada» quer
  dizer: fala o mesmo protocolo que o gateway já fala (chat completions com tool calling
  nativo), por um proxy que já foi medido, num fornecedor e numa região que o dono já aprovou.
- **Entra com uma assinatura.** O dono assina um ficheiro; não revê um PR, não espera por uma
  release.
- **Em menos de uma hora**, contada do «tenho uma chave» ao «o modelo serve a primeira fatia de
  runs em produção». O relógio inclui a qualificação.
- **Um modelo mau é recusado sozinho, em dois sítios.** Antes de entrar: o arnês dá-lhe um
  veredicto negativo e o nó recusa carregar um perfil sem veredicto positivo. Depois de entrar:
  o disjuntor tira-lhe o tráfego quando falha mais do que o limiar.
- **Cada passo deixa prova.** Fica gravado que modelo serviu cada turno, com que perfil, e
  porque é que uma fatia foi aberta ou fechada.

### Não promete

- **Não promete que qualquer modelo entra.** Um modelo que fale outro protocolo, que não tenha
  tool calling nativo, ou que exija uma forma nova de devolver estado, é engenharia: um PR, como
  hoje. A promessa da §1 do acompanhamento já o diz — «uma classe de wire nova é engenharia».
- **Não promete escolher o melhor modelo para cada passo.** Isso é a A4 (cascata). Na A3 o
  modelo novo recebe uma fatia cega do tráfego; não se olha ao passo.
- **Não promete que o modelo é bom.** Promete que passou uma bateria escrita, com limiares
  escritos. A bateria de hoje são seis casos sintéticos; o que ela não exercita não fica
  provado.
- **Não promete relaxar garantias.** Um perfil não pode desligar o veredicto do kernel, a
  mediação do Reference Monitor, a soberania nem a allowlist. Relaxar uma garantia continua a
  exigir uma segunda assinatura, e fica fora desta fase.
- **Não promete nada sobre o `aos-orq`.** O planeador chama o modelo por conta própria, fora da
  governação da rota (§2). Fica como está, e dito.
- **Não decide com um juiz probabilístico.** Nenhum modelo avalia outro modelo. O veredicto é
  contagem contra limiar (acompanhamento, §8).

## 2. O que já existe e se reaproveita

Tudo o que está nesta tabela foi lido no código da base. Onde não encontrei, está dito.

| Peça | Onde está `[CÓDIGO]` | O que falta para a A3 |
|---|---|---|
| **Perfil da rota** — o que o nó espera de um nome de modelo: modelo servido, protocolo, parâmetros, versão da projecção, se devolve estado e onde | Tabela em código, quatro entradas, todas do Kimi e nenhuma com parâmetros: `gw/route.go:173-178`. «O nó não lê perfis de ficheiro, de ambiente, de um plano, de um run nem de um pedido HTTP»: `gw/route_profile.go:28-30`. Escolhe-se pelo nome pedido, comparação exacta: `gw/route.go:181-188` | Vir de fora do binário. O próprio código já o anuncia: «o perfil como artefacto assinado do registo é da fase A3» (`gw/route.go:97`) |
| **Perfis candidatos** — o gateway aceita perfis que não estão na tabela | `ProductionConfig.RouteProfiles`: `gw/production.go:201-206`. Leitura fechada (campos desconhecidos e chaves repetidas recusados): `ParseRouteProfile`, `gw/route_profile.go:190-216`. Hoje só o banco os usa; o nó não os passa: `nó/modelgatewaywiring.go:279-283` | Quem os entrega ao nó, e a assinatura. A porta de entrada já existe |
| **Digest do perfil por turno** | `RouteProfile.Digest()`, `gw/route.go:142-167`; fica no manifesto de cada turno e no envelope do estado opaco | Nada. É a prova de «que perfil serviu este turno» |
| **Perfil que muda a meio de um run** | A projecção fica fixada por run (`nó/modelo_do_turno.go:52-69`); estado de outra rota numa rota `obrigatorio` não é enviado e o turno falha (`gw/state_return.go:236-241`) | `[NÃO ENCONTREI]` uma guarda única «o perfil mudou entre turnos deste run». Com dois modelos passa a ser preciso fixar também o **modelo** por run |
| **Allowlist de modelos assinada** — que board pode usar que modelo em que região | Embebida no binário, ed25519, com a impressão da chave fixa em código: `gw/policy/allowlist/allowlist.go:59-66`, `:81`. Regra = board × modelos × regiões, sem custo nem tier: `:117-122`. **Já há uma via externa**: uma pasta com a política e a assinatura, conferida contra uma âncora dada por ambiente (`nó/modelgatewaywiring.go:52-60`) | Serve de molde exacto para o perfil assinado. Mas um nome de modelo novo exige re-assinar a allowlist: são dois ficheiros a assinar, não um (decisão D1) |
| **Como o nó escolhe o modelo** | Um só por nó, de `AOS_MODEL_NAME`: `nó/modelo_do_turno.go:24-29`. O `POST /runs` não tem campo de modelo, de propósito: `nó/api.go:761-766`. O adaptador constrói-se uma vez: `nó/modelgatewaywiring.go:344` | Tudo o que é «mais de um modelo». É a peça maior da fase |
| **O plano e o `aos-orq`** | Um nó do plano não tem campo de modelo, tier nem classe: `packages/control-plane/orchestrator/plan/plandocument.go:119-148`. O `aos-orq` chama o modelo com um gateway seu, fora da governação da rota: `nó/model_route_env.go:22-24` | Nada nesta fase (decisão D6). Fica dito que o planeador não é coberto |
| **Registry** — catálogo versionado, assinado, com promoção | Três tipos fechados: skill, tool e servidor MCP (`reg/domain/artifact.go:17-40`). Entra em staging e só chega a activo por um verificador de admissão; assinatura ed25519 com trust store auditado (`reg/doc.go`, `reg/signing/doc.go`). Eval-gate e ratificação humana só para skills (`reg/promotion/doc.go`). **O catálogo e o pipeline de promoção não são construídos em nenhum binário**: fora do seu pacote, `registry.New` e `promotion.NewPipeline` não têm chamador. O nó usa só as peças soltas (digest, assinatura, revalidação) para um catálogo de tools em memória, com chave efémera gerada no arranque — «dev-grade», diz o código (`nó/modelcatalog.go:3-21`) | Não há tipo «perfil de modelo»; o contrato de uma entrada não tem campos de perfil (`reg/domain/contract.go:40-65`); e o Registry não corre em produção. Usá-lo para perfis é abrir estas três frentes (decisão D1) |
| **Ratificação humana assinada** | `POST /promote` no nó: o dono assina fora do nó (`aos-issuer ratify-sign`), a assinatura é de uso único, e a decisão fica selada (`nó/promotion.go:15-30`). Aceita skill e memória procedural (`nó/promotion_api.go:76-78`) | Reaproveita-se a **ferramenta de assinar** e o hábito. Atenção: `canary_passed` é um booleano que quem chama fornece (`nó/promotion_api.go:80`), e o nó avisa que o canary «continua a montante e fora do nó» (`nó/bootstrap.go:2893`) |
| **Canary** | `[NÃO ENCONTREI]` nenhum mecanismo que divida tráfego entre duas versões de coisa alguma. A palavra aparece como pré-condição declarada, não como máquina | Constrói-se de raiz |
| **Roteamento do gateway** | Failover por soberania, que nunca sai da fronteira do board (`gw/routing/failover/failover.go:1-16`); saúde injectável (`gw/production.go:411-413`); tiers e scoring determinista (ADR-021) existem no módulo. **O nó não declara tiers**: roteia só pelo failover (`nó/modelgatewaywiring.go:233-237`) | Os tiers são da A4. A A3 não precisa deles |
| **Disjuntor por modelo** | `[NÃO ENCONTREI]`. Os disjuntores que há são de outro eixo: de orçamento (`packages/control-plane/budget/rmadapter.go:82-85`) e por run (`packages/kernel/agent-runtime/breaker/`). A saúde de uma rota é uma função injectada que ninguém preenche: sem ela, tudo é tratado como saudável (`gw/production.go:411-413`) | Constrói-se de raiz. Há sinais, mas poucos têm o modelo no rótulo: só `aos_model_route_checks_total{served}` e `aos_model_route_params_rejected_total{rota}` (`nó/api.go:2278`, `:2345`). Não há contador de erros HTTP nem de latência por rota. O veredicto do kernel por run existe, sem o modelo ao lado. O único alerta de rota avisa e não actua (`deploy/server/alerta-rota.sh`) |
| **Banco de ensaio** | Três modos (falso, proxy real com falso, modelo real). Bateria de seis casos e sete nós (`banco/bateria/casos.json`). Taxas com intervalo de confiança. Tecto diário e contador persistente, contado antes de enviar. Proxy fixado por digest (`banco/proxy.go:51`). Aceita um perfil candidato e põe o digest dele no relatório (`banco/relatorio.go:100-109`) | Ver a linha seguinte |
| **Veredicto do banco** | Só existe para a devolução do estado, com regra escrita e precedência fixa (`banco/devolucao.go:350-362`, `:487-502`). Para o modelo como um todo o banco diz de si: «devolve taxas; não aceita nem recusa um modelo» (`banco/relatorio.go:185`). O relatório não é assinado nem tem digest de si próprio `[NÃO ENCONTREI]` | O veredicto do **modelo**: limiares, regra, controlos negativos, e um digest que o perfil possa citar |
| **Proxy de produção** | Tag móvel `ghcr.io/berriai/litellm:main-stable` (`deploy/server/docker-compose.prod.yml:922`); a configuração no servidor é do operador, o deploy não a reescreve | O banco mede noutra imagem (o digest fixado). Sem igualar as duas, qualifica-se contra um proxy e serve-se por outro (decisão D8) |

**Os princípios e os ADR que mandam aqui.**

- *Auto-modificação com rede* (`AGENTS.md` §7, n.º 9; ADR-012): nada que mude o comportamento
  chega a produção sem eval-gate, canary e ratificação humana assinada. Um perfil de modelo muda
  o comportamento. O caminho da §3 tem os três, por esta ordem.
- *Soberania regional* (n.º 10) e *política default-deny* (n.º 7): a allowlist continua a
  mandar. Um perfil assinado não autoriza um modelo num board ou numa região; só a allowlist o
  faz. Produção está selada para `eu-west`.
- *Fail-closed* (n.º 8): perfil com assinatura inválida, veredicto em falta ou digest que não
  bate — o nó não arranca com ele. Não «arranca sem ele em silêncio».
- **ADR-036 §2.8**: a rota que serviu o turno compara-se com um perfil. A A3 muda de onde o
  perfil vem, não o que ele é; é uma emenda a esta secção.
- **ADR-040 §2.10 e §2.11**: quando se pode ligar a captura do estado, e como se devolve. Um
  modelo que devolve estado só entra em produção depois do §2.10 cumprido (decisão D8).
- **ADR-018**: o que é do nó e o que é do `aos-orq`. A escolha do modelo de um run fica no nó.
- **ADR-021**: o scoring determinista entre modelos. Fica para a A4; a A3 não o contradiz.
- **ADR-025**: o controlador de autonomia já despromove sozinho com base em fiabilidade medida.
  É o precedente para um disjuntor que tira tráfego sem perguntar e só o devolve por acto humano.

## 3. O caminho de um modelo novo, passo a passo

Do «tenho uma chave» ao «serve runs em produção». O relógio do critério — menos de uma hora —
começa no passo 1 e pára no fim do passo 6.

| # | Passo | Quem faz | Prova que fica | Tempo esperado |
|---|---|---|---|---|
| 0 | **Pré-condições, uma vez por fornecedor.** O fornecedor e a região estão aprovados; a allowlist assinada já autoriza o nome que o nó vai pedir; o proxy de produção tem a rota configurada e é a imagem em que o arnês mede | dono | allowlist assinada; digest da imagem do proxy | fora do relógio |
| 1 | **Escrever o perfil candidato.** Um ficheiro pequeno: nome pedido, modelo esperado, protocolo, parâmetros, versão da projecção, se devolve estado. Para um modelo da mesma família, copia-se o do irmão e muda-se o nome | dono | o ficheiro, com digest | 5 min |
| 2 | **Qualificar.** Um comando. O arnês corre a sonda, a bateria com o perfil, e os controlos negativos; conta contra os limiares; escreve o veredicto | máquina | relatório com as contagens, os digests da bateria, do perfil, da rota e da imagem do proxy, e o veredicto: `qualificado`, `recusado` ou `inconclusivo`, com as razões em vocabulário fechado | 15 a 25 min `[HIPÓTESE]`: as três corridas da A2, 51 pedidos, levaram 6 minutos |
| 3 | **Recusa automática, se for o caso.** Com `recusado` ou `inconclusivo` o comando não produz nada para assinar. Acaba aqui, e o relatório diz porquê | máquina | o relatório | — |
| 4 | **Assinar.** O dono lê o resumo de uma página e assina o registo de entrada: o perfil, o digest do veredicto e a fatia inicial. É a única assinatura | dono | registo de entrada assinado (ed25519), fora do repositório | 5 min |
| 5 | **Entregar ao nó.** O ficheiro assinado vai para a pasta de perfis do servidor e o nó é recriado. No arranque o nó confere a assinatura contra a chave fixa, confere que o veredicto citado é `qualificado` e é do mesmo perfil e da mesma imagem do proxy, e declara no banner o modelo candidato e a fatia. Se alguma coisa não bate, **não arranca** | dono (copia e recria); máquina (confere) | banner; evento selado «perfil carregado», com o digest | 5 min |
| 6 | **Servir uma fatia.** O nó dá ao candidato a fatia assinada dos planos novos. Cada run fica com o modelo fixado e gravado; cada turno grava o digest do perfil | máquina | manifesto de cada turno; contadores por modelo | primeiro run em minutos, havendo tráfego |
| 7 | **Vigiar e recuar.** O disjuntor conta os desfechos do candidato. Passado o limiar, a fatia vai a zero, fica selado porquê, e o dono é avisado. Os runs em curso acabam com o modelo com que começaram | máquina | evento selado «disjuntor aberto», com as contagens | contínuo |
| 8 | **Passar a titular, ou sair.** Outro acto assinado do dono, com os números do canary à frente. Fora do relógio e fora do critério da fase | dono | segundo registo assinado | quando o dono quiser |

Três coisas a notar.

- **Zero PRs** vale para os passos 1 a 7. O passo 0 pode exigir trabalho quando o fornecedor é
  novo, e isso é de propósito: aprovar um fornecedor e uma região é uma decisão, não uma
  entrada de catálogo.
- **O dono aparece três vezes** (escrever, assinar, copiar), mas só assina uma.
- **O que o nó confere no passo 5 é o veredicto, não a palavra de quem entrega.** Hoje a
  ratificação de skills aceita `canary_passed` como um booleano de quem chama. Aqui o perfil
  cita o digest de um relatório, e o nó recusa se o relatório não disser `qualificado` para
  aquele perfil. Um veredicto que se recalcula do que o chamador declara não é um gate.

## 4. As peças a construir, pela ordem em que dão valor

Sete peças. Cada uma entra desligada ou inerte, e nenhuma muda o que produção faz hoje enquanto
não houver um perfil assinado na pasta.

### 4.1 «A3-proxy» — o proxy de produção é o proxy em que se mede

- **O que é.** Fixar a imagem do proxy de produção por digest, o mesmo em que o banco e os
  gates medem, e pôr o digest da imagem no relatório do arnês e no que o nó confere.
- **Critério de aceitação.** O compose de produção refere a imagem por `@sha256:`; o arnês
  recusa qualificar contra uma imagem diferente da declarada para produção; um teste prende a
  igualdade dos dois digests no repositório.
- **Desligado por omissão.** Não se aplica: é uma mudança de deploy, feita pelo dono, com um
  plano de verificação antes e depois.
- **Risco.** A tag `main-stable` de hoje pode já não ser a imagem medida. A mudança pode
  alterar o comportamento de produção, para o que foi medido.
- **Porque é a primeira.** Sem ela, tudo o que o arnês prova vale para outro proxy (resíduo (h)
  da A2).

### 4.2 «A3-arnês» — o banco passa a dar um veredicto sobre o modelo

- **O que é.** Uma experiência nova do banco, `qualificacao`: a bateria com o perfil candidato,
  mais os controlos negativos, mais uma regra fixa que transforma contagens em `qualificado`,
  `recusado` ou `inconclusivo`. O relatório ganha o digest de si próprio e o da imagem do
  proxy. A regra segue o molde da que já existe para a devolução do estado: razões firmes dão
  `recusado`, razões passageiras sozinhas dão `inconclusivo`.
- **Critério de aceitação.** (1) Contra cada provider falso «mau» — não chama a tool, responde
  vazio, recusa o segundo turno, troca o modelo servido, corta a resposta — o veredicto é
  `recusado`, com a razão certa: 100%, em CI, em cada PR. (2) Contra o falso «bom»,
  `qualificado`. (3) Cada limiar tem uma mutação que o desloca e avermelha um teste. (4) Com o
  Kimi de produção, no posto do dono, o veredicto é `qualificado`: se o modelo que já serve
  bem não passa, o arnês está errado. (5) Uma corrida que não coube no tecto ou parou a meio é
  `inconclusivo`, nunca `qualificado`.
- **Desligado por omissão.** O modo real continua a ser só do dono e a recusar correr em CI.
- **Risco.** Limiares mal escolhidos: apertados de mais recusam modelos bons, largos de mais
  não recusam nada. Por isso o ponto (4), e por isso os limiares são decisão do dono (D2).

### 4.3 «A3-perfil» — o nó carrega perfis assinados de uma pasta

- **O que é.** O molde da allowlist externa, aplicado aos perfis: uma pasta com o registo de
  entrada e a assinatura, uma variável com a âncora de confiança, conferência no arranque. Os
  perfis válidos entram pela porta que já existe (`ProductionConfig.RouteProfiles`). O registo
  de entrada cita o digest do veredicto; o relatório vai ao lado e o nó confere-o.
- **Critério de aceitação.** Sem pasta, o binário comporta-se byte a byte como hoje (preso por
  digest dos quatro perfis da tabela). Com assinatura inválida, âncora errada, veredicto em
  falta, veredicto que não é `qualificado`, veredicto de outro perfil ou de outra imagem do
  proxy: o nó não arranca, com causa própria para cada um. Um perfil assinado não pode
  substituir uma entrada da tabela em código sem o dizer no banner. O carregamento fica selado.
- **Desligado por omissão.** Sim: sem a variável, nada muda.
- **Risco.** Um perfil que muda com runs em curso. Hoje a projecção já fica fixada por run, e
  uma rota `obrigatorio` falha fechado; a regra operacional mantém-se — muda-se com a rota
  drenada — e passa a estar no runbook desta peça.
- **Só com esta peça** já se cumpre metade do critério para um caso estreito: trocar o modelo
  único do nó por outro, sem PR. Sem fatia e sem recuo automático.

### 4.4 «A3-dois-modelos» — o nó serve um titular e um candidato

- **O que é.** O nó passa a conhecer dois modelos: o titular (o de hoje) e, no máximo, um
  candidato. A escolha faz-se uma vez, no início do run, por uma função determinista do
  identificador do plano e da fatia assinada; fica gravada no manifesto e não muda — nem numa
  retoma, nem numa nova tentativa do mesmo nó do plano. Quem submete o run não escolhe.
- **Critério de aceitação.** Com fatia de 0% ou sem candidato, byte a byte como hoje. Com
  fatia de X%, numa série de pelo menos 200 identificadores a fracção fica dentro do intervalo
  esperado, e a mesma entrada dá sempre a mesma escolha. Todos os nós de um plano, e todas as
  tentativas de um nó, usam o mesmo modelo. Um run retomado depois de o candidato sair acaba
  com o modelo fixado ou falha com causa própria; nunca muda de modelo a meio. Os contadores
  de turnos, de desfechos e de respostas rejeitadas passam a ter o modelo no rótulo.
- **Desligado por omissão.** Sim: sem candidato assinado não há segundo modelo.
- **Risco.** É a peça que toca no caminho de todos os runs. Por isso o candidato é um só, e a
  escolha é uma função pura que se testa sem rede.

### 4.5 «A3-disjuntor» — o candidato perde a fatia sozinho

- **O que é.** Um contador por modelo, alimentado pelo que o nó já sabe sem interpretar texto:
  o veredicto do kernel («não cumprido»), a resposta vazia, a resposta rejeitada, o erro do
  provider, e o modelo servido diferente do esperado. Uma regra de limiar sobre uma janela de
  runs. Aberto, a fatia do candidato é zero para runs novos; o facto fica selado, com as
  contagens; sai um alerta. **Não volta a fechar sozinho.**
- **Critério de aceitação.** Com um provider falso que degrada a meio (bom durante N runs, mau
  depois), o disjuntor abre dentro da janela escrita e nenhum run novo vai para o candidato;
  os runs do titular não são tocados. Sobrevive a um reinício do nó: aberto continua aberto.
  Com um candidato bom, não abre em 200 runs. Uma troca do modelo servido abre-o ao primeiro
  turno. O disjuntor nunca tira o tráfego ao titular.
- **Desligado por omissão.** Primeiro em `observe`: conta e diz quando teria aberto, sem
  mexer na fatia.
- **Risco.** Com pouco tráfego a janela demora a encher, e um candidato mau serve mais tempo
  do que se quer (§8). E um limiar sensível de mais tira um modelo bom por azar: daí o
  `observe` primeiro.

### 4.6 «A3-comando» — um comando do princípio ao fim, e o runbook com relógio

- **O que é.** O encadeamento dos passos 2 a 4 da §3 num comando: corre a qualificação, mostra
  o resumo de uma página, e só com `qualificado` prepara o registo de entrada para o dono
  assinar com a ferramenta que já usa. E o procedimento escrito, com os tempos.
- **Critério de aceitação.** Com `recusado` ou `inconclusivo` não fica nenhum ficheiro por
  assinar. O resumo cabe numa página e diz, pela ordem: veredicto, razões, amostra, o que os
  controlos negativos mostraram, o que a corrida não prova, o gasto. A chave privada nunca
  entra no arnês nem no nó.
- **Desligado por omissão.** É uma ferramenta do posto do dono.
- **Risco.** Baixo. É a peça que faz a hora caber.

### 4.7 «A3-prova» — o ensaio do critério da fase

- **O que é.** Não é código: é a medição. (a) Um terceiro modelo entra pelo caminho da §3, com
  o relógio a contar e sem PR nenhum. (b) Um modelo mau de propósito — um perfil que aponta
  para um modelo sem tool calling, ou com um parâmetro que o parte — é recusado pelo arnês.
  (c) Um candidato que degrada depois de entrar perde a fatia sem intervenção.
- **Critério de aceitação.** É o da fase, medido e registado na §5 do acompanhamento: menos de
  uma hora, zero PRs, uma assinatura; a recusa (b) e o recuo (c) com a prova selada.
- **Risco.** O (c) em produção exige um modelo que falhe em produção. Faz-se com um provider
  falso atrás do proxy real, em ensaio, e diz-se que foi aí.

### O que fica de fora, de propósito

| O quê | Para onde vai | Porquê |
|---|---|---|
| Um tipo novo «perfil de modelo» no Registry, com staging e promoção | opcional; depois da A3 | O Registry não corre em produção. Montá-lo é uma fase em si. O registo assinado da 4.3 leva os mesmos campos (versão, digest, assinatura) e pode ser publicado no Registry mais tarde sem mudar de forma |
| O modelo escolhido por passo do plano, ou declarado pelo plano | A4 | É a cascata. Um nó do plano não tem hoje campo de modelo, e pô-lo lá muda o schema do plano |
| Mais de dois modelos ao mesmo tempo | A4 | O critério pede que o terceiro **entre**, não que sirvam três de uma vez |
| Tiers, scoring, custo na escolha | A4 | Existem no módulo; o nó não os declara |
| O disjuntor a reabrir sozinho (meia-abertura) | opcional | Reabrir é dar tráfego a um modelo que falhou: pede um acto humano |
| A governação da rota do `aos-orq` | fora | O planeador é outro binário, com outro gateway |
| Passagem automática de candidato a titular | fora | Mudar o modelo de todos os runs é uma decisão, com assinatura |

## 5. O que a A2 ensinou e a A3 tem de respeitar

1. **O banco deu verde falso duas vezes.** Contava como «devolvido» o que o gateway decidiu
   enviar, antes de o pedido sair; e contava como «estado capturado» um envelope só com
   identificadores. As duas vezes foi a revisão adversarial que o apanhou, não os testes.
   *Para a A3:* cada limiar do veredicto tem um controlo negativo que o avermelha e uma
   mutação presa por teste, e o arnês é revisto por alguém que não o escreveu, antes de o
   primeiro veredicto contar.
2. **`cumprida` não prova leitura.** A OpenRouter aceitou o segundo turno com e sem o
   raciocínio de volta. Um 2xx diz que o pedido não foi recusado; não diz que o campo foi lido.
   *Para a A3:* o veredicto separa «aceite» de «exigido». Num perfil que declara devolução
   obrigatória, a corrida de controlo sem devolução tem de falhar; se passar, o relatório diz
   «exigência não provada» e o resumo que o dono assina mostra-o à cabeça.
3. **Um agregador pode não exigir o que o fornecedor directo exige.** O mesmo modelo por duas
   portas são duas rotas. *Para a A3:* a qualificação é da **rota** — modelo, fornecedor,
   imagem do proxy, perfil —, não do modelo. Mudar qualquer um dos quatro é qualificar outra
   vez. O digest da rota no relatório já junta três deles; falta a imagem.
4. **O proxy transforma os pedidos, e isso mede-se por rota.** Na rota `openai/` retira
   parâmetros em silêncio ou recusa-os, conforme `drop_params`; na `anthropic/` insere um bloco
   de texto; na `openrouter/` põe o raciocínio num saco que o fornecedor não lê, e acrescenta
   cabeçalhos seus. *Para a A3:* o arnês corre sempre atrás do proxy, nunca directo ao
   fornecedor; e a peça 4.1 vem primeiro.
5. **Um perfil que muda a meio falha fechado os runs em curso.** Está no ADR-040 §2.11 e
   presa por teste. *Para a A3:* o modelo fixa-se por run; carregar um perfil novo faz-se no
   arranque, não a quente; tirar um candidato não mata os runs que ele já serve.
6. **O tecto salvou dinheiro e o contador não mentiu.** A corrida de 8 passagens foi recusada
   antes de enviar; a primeira corrida real da A2 gastou 212 unidades em respostas 429 antes
   de o banco aprender a parar. *Para a A3:* a qualificação declara o pior caso antes de
   começar, pára nas três primeiras recusas iguais, e tem um tecto seu (D7).
7. **O que se liga em produção mede-se com uma série antes e outra depois.** Foi assim em
   todas as fases, e foi o que apanhou os 32% de primeiras falhas da projecção 1.1.0.
   *Para a A3:* a fatia do candidato é a série «depois»; o titular, no mesmo período, é a
   série «antes». O disjuntor compara com um limiar fixo, não com o titular (§8).
8. **Uma amostra pequena não fecha nada.** A A2 fechou com 15 nós com tools, não 40, e a
   experiência dos separadores vale para um caso e um turno. *Para a A3:* o veredicto traz a
   amostra e o intervalo de confiança, e o limiar aplica-se ao limite do intervalo quando o
   dono assim decidir (D2).
9. **A conta sem créditos parou uma fase.** A API directa da Anthropic nunca correu.
   *Para a A3:* a sonda de um pedido antes de tudo, que já existe, e `inconclusivo` — não
   `recusado` — quando o que falhou foi a conta.

## 6. Decisões para o dono

Nove decisões. Em cada uma: as opções, a recomendação e a razão, e o que acontece se ficar por
tomar. Os números que aparecem como limiares são **propostas para discutir**, não medições.

### D1 — Onde vive o perfil do modelo, e quem o assina

| Opção | O que é | Custo | Cumpre «zero PRs»? |
|---|---|---|---|
| (a) Em código, como hoje | Tabela no binário; muda com um PR e uma release | nenhum | Não |
| (b) Registo assinado numa pasta do servidor | O molde da allowlist externa: ficheiro, assinatura ed25519, âncora fixa por configuração; o nó confere no arranque | pequeno: a porta de entrada já existe | Sim |
| (c) Artefacto do Registry | Tipo novo, staging, promoção, eval-gate e ratificação pelo pipeline do Registry | grande: o Registry não corre em nenhum binário | Sim, depois de montado |

**Recomendação: (b)**, com o registo desenhado para poder ser publicado no Registry mais tarde
sem mudar de forma. **Quem assina: o dono**, com uma chave só para perfis de modelo, fora do nó
e fora do arnês, pela ferramenta de assinar que já existe.

Razão: (b) cumpre o critério com o que já está escrito; (c) é a letra da linha da A3 — «artefacto
do registo» — mas obriga a pôr o Registry em produção primeiro, e isso não tem nada a ver com
modelos. **Fica dito que (b) é um desvio à letra da fase.**

Sub-decisão: um nome de modelo novo exige também re-assinar a allowlist. Ou se aceita que «uma
assinatura» quer dizer **um acto do dono que assina dois ficheiros com a mesma ferramenta**, ou
os nomes pedidos passam a ser aliases estáveis já autorizados, e o modelo real muda só no perfil
e no proxy — que é como produção já funciona (`gpt-4o-mini` pedido, Kimi servido). Recomendo a
segunda para a A3: nenhum nome novo, nenhuma allowlist nova.

*Se não decidir:* fica (a), e a fase não se cumpre.

### D2 — O que conta como «qualificado»

| Pergunta | Opções | Recomendação |
|---|---|---|
| Que bateria | (a) a de hoje, seis casos; (b) a de hoje mais casos novos | **(a)** para a A3. Os casos novos entram com versão nova da bateria, e um veredicto vale para a versão que cita |
| Quantas amostras | (a) 1 passagem, 5 nós com tools; (b) 8 passagens, 40 nós com tools; (c) 20 passagens, 100 | **(b)**: é o número que o critério P3 da A2 pedia e não se cumpriu. Pior caso declarado de 673 pedidos; reais, cerca de 100, pelas corridas da A2 |
| Que limiares | Proposta: todos os runs com tools cumpridos no fim das tentativas; no máximo 4 de 40 nós sem tool call à primeira; zero respostas 4xx nos segundos turnos; zero respostas vazias no fim das tentativas; modelo servido igual ao esperado em todos os turnos | Os valores são do dono. O primeiro e os três últimos são «zero falhas»; o segundo é o mesmo 10% que serviu para ligar a projecção 1.2.0 |
| Que controlos negativos | (1) os falsos «maus» em CI, sempre; (2) com o modelo real, o Kimi de produção tem de sair `qualificado`; (3) num perfil com devolução obrigatória, a corrida sem devolução tem de falhar, senão «exigência não provada» | **Os três.** O (3) não bloqueia a entrada: aparece à cabeça do resumo que o dono assina |
| Limiar fixo ou por classe | (a) um só para todos; (b) por classe de modelo | **(a)** agora. Só há uma classe qualificada em produção; limiares por classe decidem-se com a segunda |

Uma honestidade sobre os números: com 40 nós e zero falhas, o que se pode afirmar com 95% de
confiança é que a taxa verdadeira de falha está abaixo de cerca de 9%. Para afirmar «abaixo de
2%» seriam precisas perto de 190 amostras sem falha. A qualificação apanha um modelo **mau**;
quem mede se é **bom** é a fatia em produção.

*Se não decidir:* o arnês não tem regra, e a peça 4.2 não se pode escrever.

### D3 — Onde corre o modo real do arnês

| Opção | Chaves | Soberania | Custo e risco |
|---|---|---|---|
| (a) No posto do dono, como hoje | Ficam no ficheiro do dono | Só documentos de teste saem | Depende de o dono estar ao computador. É o que existe |
| (b) No CI | Passam a ser segredos do CI | Igual | Qualquer PR pode gastar a chave; o modo real recusa hoje correr em CI, de propósito |
| (c) No servidor de produção | Vão para o Vault do servidor | Corre dentro da região | Pedidos de ensaio a sair do servidor de produção; mais superfície |

**Recomendação: (a).** A hora do critério cabe: a qualificação são 15 a 25 minutos de máquina.
(c) faz sentido quando a qualificação tiver de se repetir sozinha (um modelo que o fornecedor
actualiza por baixo do mesmo nome), e isso não é desta fase.

*Se não decidir:* fica (a), que é o comportamento de hoje.

### D4 — Como é o canary em produção

| Pergunta | Opções | Recomendação |
|---|---|---|
| Percentagem de quê | (a) de runs; (b) de planos; (c) de turnos | **(b) planos.** Todos os nós e todas as tentativas de um plano com o mesmo modelo: senão não se sabe a quem atribuir uma falha, e o nó seguinte recebia a saída de outro modelo. Um run sem plano conta como um plano de um nó. Nunca (c): mudar de modelo a meio de um run parte o estado opaco |
| Quem escolhe | (a) uma função do identificador do plano; (b) quem submete | **(a).** Quem submete não escolhe o modelo: o `POST /runs` não tem esse campo, de propósito |
| Que fatia | 5%, 10%, 25%, ou só a pedido | **10%**, escrita no registo assinado. Mudar a fatia é assinar outra vez |
| Quanto tempo | Até N planos servidos, ou até o dono decidir | Até **40 planos** servidos pelo candidato; depois o dono decide com os números. Não passa a titular sozinho |

*Se não decidir:* não há fatia; o modelo novo só entra como substituto total do titular (o caso
estreito da peça 4.3).

### D5 — O disjuntor

| Pergunta | Opções | Recomendação |
|---|---|---|
| O que conta como falha | Proposta: run «não cumprido» no fim das tentativas; resposta vazia no fim das tentativas; resposta rejeitada; erro do provider que não seja limite de ritmo; modelo servido diferente do esperado | Todos, cada um com contador próprio. O limite de ritmo fica de fora: é da conta, não do modelo |
| Quando abre | (a) k falhas nas últimas n; (b) taxa acima de x% com amostra mínima | **(a)**: 3 falhas nos últimos 20 runs do candidato; e ao **primeiro** turno com modelo servido diferente. Com pouco tráfego uma taxa não tem amostra |
| O que faz ao abrir | (a) fatia a zero para runs novos; (b) também mata os runs em curso | **(a).** Os runs em curso acabam com o modelo fixado; matá-los perde trabalho e não desfaz o que já foi feito |
| Como reabre | (a) só por acto do dono; (b) sozinho, depois de um tempo | **(a)** |
| Primeiro passo | `observe` (conta, não actua) ou logo `enforce` | **`observe`** durante a primeira fatia, depois `enforce` por decisão do dono |

*Se não decidir:* um candidato mau fica com a sua fatia até alguém reparar. A metade «um modelo
mau é recusado sozinho» fica cumprida só antes de entrar.

### D6 — «Mais de um modelo por nó» entra nesta fase?

| Opção | O que dá | Custo |
|---|---|---|
| (a) Não: um modelo por nó, trocável sem PR | Zero PRs e uma assinatura. Sem fatia, sem recuo automático depois de entrar | Só as peças 4.1, 4.2, 4.3 e 4.6 |
| (b) O mínimo: um titular e um candidato | O critério inteiro | Mais as peças 4.4 e 4.5 |
| (c) N modelos, escolhidos por passo ou pelo plano | A cascata | É a A4 |

**Recomendação: (b).** (a) não cumpre «um modelo mau é recusado sozinho» depois de entrar: sem
segundo modelo não há para onde recuar. (c) é outra fase.

*Se não decidir:* fica (a).

### D7 — O orçamento de uma qualificação

| O quê | Proposta | Razão |
|---|---|---|
| Pedidos por qualificação | Tecto próprio de 300 reais, com o pior caso declarado antes de começar (673 para 8 passagens) | As corridas da A2 fizeram cerca de 12 pedidos por passagem. O banco recusa hoje uma corrida cujo **pior caso** não caiba no que resta do dia: foi isso que parou as 8 passagens com o tecto de 200 |
| Dinheiro por qualificação | 3 USD num fornecedor pago ao pedido | A A2 gastou cerca de 0,64 USD em 51 pedidos; 8 passagens e os controlos ficam perto de 2,5 USD pela mesma conta `[HIPÓTESE]` |
| Tecto diário | Mantém-se o do dono, por fornecedor | Uma qualificação que falha e se repete não pode esvaziar a conta |
| Quantas tentativas | No máximo duas qualificações por perfil por dia | Repetir até passar é escolher o resultado |

Há uma escolha escondida aqui: ou o tecto diário sobe para caber o pior caso de 8 passagens, ou
o banco passa a contar pelo esperado em vez do pior caso. **Recomendo subir o tecto e manter a
conta pelo pior caso:** é a conta que nunca gasta mais do que disse.

*Se não decidir:* valem os tectos de hoje, e 8 passagens não cabem na OpenRouter.

### D8 — Os resíduos da A2 que bloqueiam

| Resíduo | Bloqueia o quê | Opções | Recomendação |
|---|---|---|---|
| A imagem do proxy de produção é uma tag móvel | Toda a qualificação: mede-se num proxy e serve-se por outro | (a) fixar por digest antes de tudo; (b) aceitar e declarar | **(a).** É a peça 4.1 |
| `drop_params` no servidor | Qualquer perfil com parâmetros: com `true`, o proxy retira-os em silêncio | (a) pôr `false` agora; (b) só quando entrar um perfil com parâmetros | **(a)**, com o plano de verificação antes e depois que o runbook já descreve |
| A captura do estado por ligar; smoke sobre JetStream por correr | Só os modelos que **devolvem estado** | (a) fazer já; (b) limitar a entrada automática, para já, a perfis que não devolvem estado | **(b)** para cumprir o critério da fase, com o smoke como primeiro trabalho a seguir. O nó recusa carregar um perfil que devolve estado enquanto a captura estiver desligada |
| A governação da rota em `observe` | O disjuntor: «modelo servido diferente» só se sabe com a comparação ligada | (a) `enforce` já; (b) fica `observe`, e o disjuntor lê o contador | **(b)** agora. `enforce` é uma decisão que já está por tomar no acompanhamento, e não depende desta fase |
| A devolução obrigatória só provada com provider falso | A classe «com raciocínio a devolver» em produção | (a) pôr créditos na conta directa e correr; (b) deixar | **(a)**, quando o dono quiser a classe em produção. Não bloqueia a A3 |

*Se não decidir:* a primeira linha bloqueia a fase; as outras limitam que modelos podem entrar.

### D9 — Qual é o terceiro modelo, e em que região

| Opção | Região | O que prova |
|---|---|---|
| (a) Outro modelo do Kimi, pela mesma conta | A de hoje, já aprovada | O caminho inteiro, em produção, sem decisão de soberania nova |
| (b) O Claude pela OpenRouter | Sem região UE por esta via | O caminho até ao passo 4, só em ensaio. Não pode servir produção, que está selada para `eu-west` |
| (c) Um modelo por um parceiro com endpoint na UE | UE | O caminho inteiro e a segunda família em produção. Exige conta e aprovação do fornecedor: é o passo 0, fora do relógio |

**Recomendação: (a) para a prova do critério**, e (c) como o primeiro modelo «a sério» depois
dela. O Kimi `k3` já tem perfil na tabela em código, pelo que não serve de prova de «zero PRs»:
o modelo servido tem de ser um que a tabela hoje não conheça. Pela sub-decisão da D1, pede-se
por um alias que a allowlist já autoriza (hoje há dois para o board da UE, `gpt-4o-mini` e
`gpt-4o`), e o perfil assinado substitui a entrada da tabela para esse alias, dito no banner.
Com só dois aliases autorizados, o candidato ocupa o segundo; um terceiro modelo em simultâneo
já pedia allowlist nova. O dono indica o modelo.

A decisão da região da segunda família, que vinha da A2, fica aqui: enquanto não houver
endpoint na UE, o Claude continua só em ensaio.

*Se não decidir:* constrói-se tudo e a prova fica por fazer.

## 7. Proposta de tickets

Sem número. A numeração faz-se depois das decisões, com `scripts/ci/sessoes.py reservar`.

| Rótulo | Título | Uma linha | Depende de |
|---|---|---|---|
| «A3-proxy» | O proxy de produção fixa-se pelo digest em que se mede | Compose por `@sha256:`, igualdade presa por teste, digest da imagem no relatório do banco; `drop_params: false` no servidor, por decisão do dono | D8 |
| «A3-arnês» | O banco de ensaio dá um veredicto sobre o modelo | Experiência `qualificacao`: bateria, controlos negativos, regra fixa, `qualificado`/`recusado`/`inconclusivo`, digest do relatório; falsos «maus» em CI | D2, D7 |
| «A3-perfil» | O nó carrega perfis de rota assinados de uma pasta e confere o veredicto que citam | Registo de entrada assinado, âncora por configuração, falha fechado no arranque; emenda ao ADR-036 §2.8; inerte sem pasta | D1; «A3-arnês» |
| «A3-dois-modelos» | O nó serve um titular e um candidato, com o modelo fixado por run | Escolha determinista por plano e fatia assinada; o modelo no manifesto e no rótulo dos contadores; ADR novo | D4, D6; «A3-perfil» |
| «A3-disjuntor» | O candidato perde a fatia sozinho quando falha mais do que o limiar | Contadores por modelo, regra de janela, estado durável e selado, `observe` antes de `enforce`, alerta | D5; «A3-dois-modelos» |
| «A3-comando» | Qualificar e preparar a assinatura num comando; o runbook com relógio | Encadeia a qualificação e o resumo de uma página; sem veredicto positivo não há nada para assinar | D3; «A3-arnês», «A3-perfil» |
| «A3-prova» | O terceiro modelo entra em menos de uma hora; um modelo mau é recusado | A medição do critério da fase, registada no acompanhamento | D9; todos os anteriores |

**Quantos PR, com honestidade.** Sete tickets, **nove a onze PR**: um por ticket, mais um
segundo para o «A3-dois-modelos» (o nó e as métricas separam-se bem), mais um a três de
correcções depois das revisões adversariais — na A2 cada ticket que tocou no gateway teve pelo
menos um. Se a D6 ficar em (a), são quatro tickets e cinco a seis PR. O «A3-dois-modelos» é o
maior e o mais arriscado; os outros são pequenos ou médios.

**Ordem.** «A3-proxy» e «A3-arnês» primeiro, em paralelo: dão valor sozinhos, mesmo que a fase
parasse aí. Depois «A3-perfil». Depois «A3-dois-modelos» e «A3-disjuntor», por esta ordem. O
«A3-comando» pode andar ao lado do «A3-perfil». A «A3-prova» fecha.

## 8. O que este desenho não sabe

Perguntas que só uma medição responde.

- **Quanto tempo leva uma qualificação de 8 passagens com os controlos.** A estimativa de 15 a
  25 minutos extrapola de três corridas pequenas da A2, num só fornecedor e numa só noite.
- **Se o Kimi de produção passa nos limiares propostos.** Se não passar, os limiares estão
  errados — e só se sabe correndo.
- **Se a bateria distingue um modelo mau de um bom.** Seis casos sintéticos, documentos
  curtos. Um modelo pode passar a bateria e falhar com documentos reais. Só a fatia o diz.
- **Se há tráfego para um canary querer dizer alguma coisa.** As séries de produção têm sido
  lançadas à mão, 20 a 60 planos de cada vez. Com 10% de fatia, 40 planos do candidato pedem
  400 planos ao todo. Pode ser preciso uma série de validação lançada de propósito, ou uma
  fatia maior durante uma janela curta. Não sei qual; depende do uso real.
- **Se o disjuntor com «3 em 20» dispara por azar num modelo bom.** Com a taxa de falha à
  primeira de 1,2% que o Kimi tem hoje, a probabilidade é pequena, mas não a medi. O `observe` mede-a.
- **O que a imagem `main-stable` de produção é hoje.** Não li o servidor. Pode ser o digest
  medido ou outro.
- **Se um nome de modelo com ponto cabe em todo o lado.** Com a escada de tiers armada, o nome
  do modelo entra num identificador de stream, que não pode ter ponto
  (`gw/production_routing.go:114-123`). O nó não arma tiers hoje, e o nome pedido é um alias
  sem ponto; mas `anthropic/claude-sonnet-4.5` tem um. Não segui o nome por todos os sítios
  onde é usado.
- **Onde fica durável o estado do disjuntor.** Proponho um evento selado, lido no arranque; não
  verifiquei que stream o deve levar, nem o custo de o ler.
- **Como o nó confere um relatório que foi produzido noutro computador.** O relatório vai ao
  lado do registo assinado, e a assinatura do dono cobre o digest dele. Isso prova que o dono
  viu aquele relatório; não prova que o arnês correu de facto. Fechar isso pede o arnês a
  assinar com chave própria, e essa chave teria de viver no posto do dono. Não está neste
  desenho.
- **O ADR-012 e o código não dizem o mesmo sobre a ratificação de produção.** O ADR ainda
  afirma que o gate de produção não tem chamador; o nó compõe-o. Não é desta fase, mas quem
  escrever o ADR da A3 vai citá-lo.
- **Se já existe, noutro ramo, um ADR com o número seguinte.** O número reserva-se na altura.

## 9. O que esta nota não verificou

- O servidor de produção: nada foi lido lá. O que se diz do `.env`, do `config.yaml` e da
  imagem do proxy em vigor vem do repositório e do acompanhamento.
- O caminho completo de `AOS_MODEL_NAME` dentro do nó, para lá de onde é lido e de onde o
  adaptador é construído. A peça 4.4 começa por aí.
- O corpo do ADR-017 e do ADR-021, lidos só pelo título e pelas secções citadas.
- Que o relatório do banco tem tudo o que o veredicto precisa: li a estrutura, não a derivei
  campo a campo.
- As referências ficheiro:linha foram conferidas por amostragem, não todas.
