# ADR-033 — O emissor automático vive no servidor, e é o NÓ que o limita: o mandato

- **Estado:** Aceite — §2.1, §3 e §5 **emendados** (2026-09-26, AOS-446): quem contorna o
  mandato não é só root no host, e a ponte TLS dava root a quem escrevesse num ficheiro do `aos`
  (§6). §2.1 e §5 **emendados de novo** (2026-09-27, AOS-439, aditivo): o mandato enumera os
  `requesters` por quem o emissor pode agir (§7; decisão em ADR-035). §6.5 **respondida**
  (2026-09-27, AOS-446 **fase 1**): as âncoras de confiança ficam **seladas no WORM** e a chave que
  aceitou cada mandato entra no selo de **cada decisão**; o mandato pode ser assinado por uma chave
  **FIDO2** `sk-ssh-ed25519` (§8)
- **Data:** 2026-09-25
- **Ticket:** AOS-427
- **Substitui:** ADR-032 **§2.2** (onde vive a autoridade de emissão). As §2.1, §2.3 e §2.4 do
  ADR-032 mantêm-se; este ADR responde às perguntas 1, 2, 3 e 4 da §5 dele.
- **Relacionados:** ADR-003 (cadeia `on-behalf-of` com raiz humana), ADR-006 (credential broker
  JIT), ADR-016 §1 (assinar em nome do humano exige material do humano), ADR-027 §2.2 (o nó não
  confia em `iss:aos-orq`)

## 1. Contexto

O ADR-032 decidiu que a cunhagem sem operador assenta numa delegação de longa duração, assinada
uma vez por um humano, e que a autoridade de emissão seria **externa**, com `crypto.Signer` sobre
Vault. Rejeitou pôr o emissor a correr no servidor, porque isso obrigaria a chave — ou o token do
Vault que lhe dá acesso — a viver lá (§2.2).

Ao desenhar a implementação, mediu-se o que essa rejeição pressupunha, e o pressuposto não se
aguenta:

| Facto medido | Onde |
|---|---|
| O Vault de produção corre **no mesmo servidor** que o nó | `deploy/server/docker-compose.prod.yml`, serviço `vault` |
| E **destrava-se sozinho** — o material de unseal está no disco do servidor | `secrets/vault-init.json`, serviço `vault-unseal` |
| O nó confia no emissor **por inteiro**: verifica a assinatura e aceita a raiz `human:<user_id>` que o token afirma | `identity/verifier.go`, passo 6 |
| O `aos-issuer mint --human` **já cunha sem browser** (`auth_method manual`) | `aos-issuer/main.go` |

A consequência é que «emissor externo com a chave no Vault» e «emissor no servidor» **dão a mesma
protecção contra quem compromete o servidor: nenhuma.** Com a chave no Vault desse servidor, quem
tiver o servidor pede assinaturas ao Vault. O que o ADR-032 §2.2 protegia era a chave; o que
importava proteger era o **poder de cunhar**.

E o que falta para cunhar sem operador não é a capacidade — o `mint --human` já a tem. Falta o
**limite** ao que se cunha, a **revogação** e a **prova** de quem autorizou.

## 2. Decisão

### 2.1 O limite vive no NÓ, e é o mandato

A «delegação de longa duração» do ADR-032 §2.1 materializa-se num **mandato**
(`identity.Mandate`): um documento que o humano assina com a **sua própria chave**, e que fixa
exactamente o que um emissor automático pode cunhar em seu nome:

| Campo | O que fixa |
|---|---|
| `human`, `board` | em nome de quem, e sob que fronteira de soberania |
| `agent_id`, `agent_class`, `policy_ref` | que agente — incluindo a política, porque o PDP a lê |
| `scope` | o tecto do escopo: cada token tem escopo ⊆ este |
| `iss` | o único emissor autorizado |
| `max_ttl_s` | o TTL máximo de cada token, ≤ `identity.TTLMaximo` |
| `nbf`, `exp` | a janela em que se pode cunhar, ≤ `identity.MandatoValidadeMaxima` (90 dias) |
| `requesters` | **(emenda AOS-439, §7)** por quem o emissor pode agir: o `sub` de cada submissor de planos, sem curingas; o nó recusa um run cujo submissor — derivado por ele, ADR-035 — não esteja aqui |

**Nenhum campo é opcional.** Um campo vazio seria um curinga, e o mandato existe para não haver
curingas.

O mandato viaja **embebido em cada token** que se cunha sob ele (`Claims.Mandate`), e o **nó**
verifica-o contra a chave do humano **pinada no nó** (`AOS_MANDATE_SIGNERS`). O emissor automático
entra como um **segundo trust anchor** (`AOS_MANDATED_ISSUER_ID` + `_PUBKEY`), que o
`identity.Verifier` **só aceita dentro de um mandato** (`WithMandatedIssuer`).

Com isto, o raio de acção de um **emissor** comprometido passa a ser **exactamente o mandato**.
Um atacante com o contentor do emissor, o token do Vault que ele usa ou a chave transit pode pedir
assinaturas, mas:

- cunhar fora do mandato ⇒ `E_MANDATE_VIOLATED`;
- assinar um mandato seu, com uma chave gerada por ele ⇒ `E_MANDATE_INVALID`, porque a chave não
  está pinada;
- cunhar sem mandato ⇒ `E_MANDATE_REQUIRED`.

A chave do humano **nunca esteve no servidor**. É por ela ser pinada no nó que o mandato limita
alguma coisa.

**O que o mandato NÃO cobre, e é preciso dizê-lo com as mesmas letras:** root no host onde corre
o **nó**. O nó e o emissor partilham o servidor, e `AOS_MANDATE_SIGNERS` vem do `.env` desse
servidor: quem tiver root troca a chave pinada (ou o `AOS_ISSUER_PUBKEY`) e reinicia o nó. Nenhuma
verificação dentro de um processo protege contra quem reescreve o processo. O mandato transforma
«comprometer o emissor» de catastrófico em limitado — não transforma «root no anfitrião» em nada.

**Emendado (AOS-446):** «root no host» é o nome curto de um conjunto maior. O `.env` é do `aos`,
não do root; o `aos` está no grupo `docker`; a chave de deploy e quem aprova o environment
`production` agem como o `aos`; e a máquina do operador guarda a chave do humano **e** a
`issuer.key`, que cunha sem mandato nenhum. O conjunto, com os meios, está na §6.1.

O mesmo vale para o TTL: o nó amarra-o ao **seu** relógio (`nbf == iat`, e o passo 4 do `Verify`
recusa um `nbf` futuro), porque o `iat` é uma afirmação do emissor. Sem isso, um emissor
comprometido cunhava um token de 45 minutos válido até ao fim do mandato — achado da revisão
adversarial, fechado antes do merge.

### 2.2 O emissor automático corre no servidor, com a chave no Vault transit

**Substitui o ADR-032 §2.2.** O `aos-issuer mint-mandated` corre no servidor, por timer, com a
chave `aos-issuer-auto` no Vault transit — a chave nunca entra no processo, e o token do Vault que
ele usa só pode **assinar** com essa chave.

Não tem nenhuma flag de identidade: humano, board, agente, classe, política e escopo vêm todos do
mandato. Verifica o mandato contra a mesma chave pinada **antes** de pedir uma assinatura ao Vault,
e escreve o token de forma atómica no ficheiro que o `aos-orq consume` relê a cada submissão.

**Alternativa rejeitada: o emissor na máquina do operador**, por tarefa agendada, a empurrar o
token por uma chave SSH presa a um comando forçado (o molde do `backup-pull-gate.sh`). Mantinha o
ADR-032 §2.2 intacto com pouco código. Custo que a fez perder: com o tecto de 1h da biblioteca, a
fila parava ~45 min depois de a máquina adormecer — não era «sem operador», era «sem operador
enquanto o portátil estiver ligado».

### 2.3 A revogação é do MANDATO, pelo registo que já existe

Revogar um mandato mata **todos** os tokens cunhados sob ele, **sem saber os seus `jti`**:
`POST /nhi/revoke` com `jti=mandate:<id>`. Usa o mesmo registo durável dos `jti`
(`identity.Revocations`), num espaço de nomes que não colide — um `jti` é base64url, sem `:`.

Responde à pergunta «como se revoga» do ADR-032 §5.2, que era a mais cara: hoje só havia revogação
de TOKEN, e um mandato de 30 dias sem revogação seria uma credencial de 30 dias.

A outra via de revogação é administrativa: tirar o humano de `AOS_MANDATE_SIGNERS` e reiniciar
invalida todos os mandatos dele.

### 2.4 Os tectos são da biblioteca, pela razão do ADR-032 §2.4

`MandatoValidadeMaxima = 90 dias` e `max_ttl_s ≤ TTLMaximo` são validados na **assinatura**
(`SignMandate` recusa) **e** na **verificação** (o nó recusa). São constantes: um mandato esquecido
acaba sozinho, e um deployment novo não levanta o tecto.

### 2.5 As colisões que anulariam o limite abortam o arranque

| Colisão | Porque anula |
|---|---|
| `AOS_MANDATED_ISSUER_PUBKEY` == `AOS_ISSUER_PUBKEY` | o automático assinaria com o `iss` do manual e verificaria **sem mandato** |
| a chave de um humano pinado == a do emissor | quem cunha escreveria os mandatos |
| `AOS_MANDATED_ISSUER_ID` == `AOS_ISSUER_ID` | dois emissores com um nome |
| uma das três variáveis sem as outras | um nó que anuncia o limite sem o impor |

Todas abortam com `ErrBadMandatedIssuer`, e o banner de arranque declara a postura.

## 3. Respostas ao ADR-032 §5

| Pergunta | Resposta |
|---|---|
| 1. Onde corre o emissor | No servidor, por timer, com a chave no Vault transit (§2.2). **Push**: o emissor escreve o ficheiro que o `aos-orq` relê |
| 2. O formato da delegação | O mandato (§2.1): enumerado campo a campo, sem curingas, revogável por id (§2.3), com validade ≤ 90 dias; renova-o o humano, assinando outro |
| 3. De onde vem a chave que assina | Da máquina do humano, num ficheiro de seed ed25519 **em hex, em claro** — sem cifra nem passphrase (`lerSeedHumana`, `aos-issuer/mandato.go`; emenda AOS-446). **Resíduo**: não é hardware (ADR-016 §1 na forma, não no espírito); quem copia o ficheiro assina mandatos |
| 4. Onde vive o sensor | No servidor, ao lado de quem cunha, no molde do `alerta-ancora.sh` — é a entrega operacional que segue |

## 4. Consequências

**Ganha-se:** a cunhagem sem operador deixa de depender de a chave estar longe do servidor. A
defesa passa a ser uma verificação no nó, testada e com mutação provada, em vez de uma propriedade
de topologia que ninguém verificava.

**Paga-se:**

- **Um processo novo em produção** (o timer de cunhagem), como o ADR-032 já previa.
- **Tokens maiores**: o mandato vai em cada token (centenas de bytes).
- **A chave do humano é um artefacto de alto valor** — quem a tiver assina mandatos. É o preço
  que o ADR-032 §2.1 já tinha escrito.
- **O ADR-032 §2.2 deixa de valer.** O raciocínio dele estava certo para a chave; errado sobre o
  que a chave protegia.

## 5. Resíduos declarados

1. **A chave do humano é uma seed em ficheiro**, em hex e **em claro** (sem passphrase — emenda
   AOS-446), não hardware. Um mandato assinado por WebAuthn/passkey fecharia isto; não existe no
   repositório nenhuma via de assinatura por hardware. A Fase 1 do AOS-446 (mandato assinado por
   FIDO2 `sk-ssh`) é a via decidida; até lá, a mitigação é de custódia (§6.4).
2. **Uma cunhagem que nunca é usada não deixa rasto.** O `mint-mandated` corre sem Event Store,
   logo não há `identity.nhi.issued`. Um token só aparece no registo quando chega ao nó. Um
   atacante que cunhe dentro do mandato e não use o token não se vê — mas também não fez nada.
3. ~~**O `Principal.MandateID` não é ainda selado nos registos de decisão.**~~ **FECHADO pelo
   AOS-439** (§7): o `mandate_id` entra no selo de cada decisão (WORM v4) e no evento de mediação.
4. **Dentro do mandato, um emissor comprometido cunha à vontade.** O mandato limita o que se
   cunha, não quantas vezes. É por isso que o escopo e a janela devem ser os mínimos que o
   trabalho precisa.
5. **Root no host do nó não é coberto** (§2.1) — **e root é só um dos seis** (emenda AOS-446,
   §6.1): o `aos`, a chave de deploy, quem aprova o environment `production` (e os administradores do
   repositório), a máquina do operador e quem é cluster-admin contornam-no igualmente. Separar o nó do emissor em anfitriões diferentes, ou pinar as chaves
   humanas num sítio que root no anfitrião não reescreve, fecharia a parte do host; nenhum dos dois
   existe. A Fase 1 do AOS-446 (as âncoras de confiança registadas no WORM) é a resposta decidida:
   um pino trocado deixa de ser silencioso. E o pino não é só `AOS_MANDATE_SIGNERS` (§6.3).
6. **O `jti` do emissor mandatado é escolhido por ele**, logo a revogação por TOKEN não o trava:
   contra ele revoga-se o MANDATO.
7. **Todos os planos drenados correm sob o humano do mandato**, seja quem for que os submeteu
   (AOS-437, revisão adversarial M4). O `POST /plans` grava o `principal` de quem pede; os runs
   levam o NHI do humano do mandato, e a cadeia on-behalf-of termina nele. Quem assina um mandato
   para o drenador responde pelo que qualquer submissor autorizado pede — é por isso que o escopo
   do mandato deve ser o mínimo do caminho do plano.
   **Emendado (AOS-439, §7):** o humano do mandato continua a ser a raiz da cadeia, mas só responde
   pelos submissores que NOMEOU nos `requesters`; o submissor de cada run fica selado como
   `requested_by`. O resíduo passa a ser a janela de migração dos mandatos v1 (§7.2).

## 6. Emenda (AOS-446, 2026-09-26) — a fronteira do host

O AOS-446 abriu os três limites da §5 que não tinham ticket. O desenho mediu-os no código e em
produção (2026-09-26), e três afirmações deste ADR — e uma do próprio AOS-446 — eram curtas.

### 6.1 Quem contorna o mandato

| Quem | Como | Onde se lê |
|---|---|---|
| **root** no host | reescreve o `.env` e reinicia o nó (§2.1) | — |
| o utilizador **`aos`** | é **dono** do `/opt/aos/.env`, que traz o pino (§6.3); e está no grupo `docker`, que equivale a root | `deploy/server/bootstrap.sh` (passos 2 e 3) |
| a **chave de deploy** (`DEPLOY_SSH_KEY`) | é shell no `aos` | `.github/workflows/deploy.yml` |
| quem **aprova o environment `production`** — e os **administradores do repositório GitHub**, que mudam as regras desse environment e os seus secrets | o deploy corre, como o `aos`, o `deploy.sh`, o compose e os scripts do commit aprovado — e o `aos` é root pelo `docker`; quem muda as regras dispensa a aprovação, e quem lê ou troca os secrets tem a chave de deploy | `.github/workflows/deploy.yml` |
| a **máquina do operador** | guarda a `humano-mandato.key` (assina qualquer mandato) e a `issuer.key` (o emissor **manual**, que o nó aceita por inteiro e **sem mandato**, §2.5), na mesma pasta `secrets-local/` que as duas seeds do *four-eyes*, a `wormseal.key` e a chave dos backups; e tem **SSH como root** no servidor (o `bootstrap.sh` e o procedimento de root correm por `ssh root@…`) | `deploy/server/README.md` §Onde vive cada chave |
| quem é **cluster-admin** no Kubernetes, ou cria **pods privilegiados / `hostPath`** que aterrem neste nó | o host é um nó **control-plane** do cluster (apiserver, etcd e kubelet a correr nele — `deploy/server/README.md` §O servidor real, `bootstrap.sh` passo 5; o `/etc/kubernetes/admin.conf` é o do kubeadm). Um pod privilegiado ou com `hostPath: /` neste nó é root no host, e daí reescreve o `.env`. **Inferência, não verificado em produção:** que um pod assim possa ser agendado aqui — o *taint* de control-plane do kubeadm afasta pods sem *toleration*, e cluster-admin pode dá-la | `deploy/server/README.md` §O servidor real |

O mandato limita **o emissor automático**. Não limita nenhum destes seis, e a §2.1 nomeava um.

### 6.2 F3 — a ponte TLS dava root e cluster-admin a quem escrevesse num ficheiro do `aos`

Verificado em produção: o `aos-tls-sync.service` corria como **root** (sem `User=`), com
`KUBECONFIG=/etc/kubernetes/admin.conf`, o executável `/opt/aos/sync-tls.sh` — do `aos` (0755) e
**reescrito pelo CD a cada deploy**. Quem escrevesse naquele ficheiro ganhava root no host e
cluster-admin no cluster na passagem diária seguinte, sem tocar no servidor. O script tinha ainda um
`chown` do root sobre caminhos da pasta do edge, que é do `aos`: um symlink plantado entre o `mv` e o
`chown` entregava ao `aos` qualquer ficheiro do sistema.

**Fechada no repositório pela Fase 0 do AOS-446**, e em produção quando o dono correr o
procedimento de root (`deploy/server/README.md` §TLS, «Instalar como root»):

- o executável que o root corre vive em `/usr/local/sbin/aos-sync-tls` (root:root 0755), instalado
  pelo root a partir de uma cópia verificada, e **sai do rsync do deploy**; o gate `lint`
  (entrega do servidor) passa a exigir, a toda a unidade root, `ExecStart=/usr/local/sbin/aos-<nome>`
  com a fonte **fora** do deploy;
- o `admin.conf` dá lugar à ServiceAccount `aos-tls-sync` (`deploy/server/tls-sync-rbac.yaml`), cujo
  único poder é `get` no secret `default/aos-node-tls`; o script recusa o `admin.conf` e um
  kubeconfig que não seja root:root 0600;
- tudo o que o script lê ou escreve na pasta do edge faz-se **como o `aos`** (`runuser`): o root não
  abre nenhum caminho que o `aos` controle;
- o kubeconfig é uma **allowlist exacta** (o `controller-manager.conf` e o `scheduler.conf` do
  kubeadm também são credenciais largas, e uma lista de proibidos deixava-os passar), e o script
  recusa uma credencial que consiga `list secrets`;
- `TestAOS446_UnidadesRootForaDoAlcanceDoAos` e `TestAOS446_SyncTLSNaoRecuaParaOAdminConf`
  (`packages/cmd/aos-issuer`) avermelham se uma unidade root voltar a apontar para `/opt/aos`
  (executável, linha de comando, `EnvironmentFile`, `WorkingDirectory`, qualquer `Environment=`),
  fixar `BASH_ENV`, `ENV`, `LD_*` ou `PATH`, fixar um `KUBECONFIG` que não seja o mínimo, repetir
  `User=`, ou se a fonte do executável voltar ao rsync em qualquer forma (nome, glob, directório
  inteiro) — provados contra a unidade que estava em produção e contra uma mutação por caso.

**Da mesma classe, fechada na mesma fase:** o procedimento da cunhagem (AOS-437) instalava as
unidades da cunhagem e da drenagem por glob a partir de `/opt/aos/systemd/`, que é do `aos`. O glob
apanhava um symlink plantado lá (`aos-cunhar-nhi.conf -> /etc/kubernetes/admin.conf`), e o `install`
seguia-o e copiava o cluster-admin para `/etc/systemd/system/` a `0644`; e um `User=root` escrito
pelo `aos` passava. Confirmado num contentor pela revisão de segurança; em produção
`/etc/systemd/system` só tinha as seis unidades esperadas. As seis unidades passam a entrar no
mesmo pacote verificado por hash e a instalar-se **pelo nome**; o `/opt/aos/systemd/` deixa de ser
fonte do root.

**O que a F3 fechada NÃO muda, e é preciso dizê-lo:** enquanto o `aos` estiver no grupo `docker`, o
conjunto da §6.1 é o mesmo. A F3 e a instalação por glob eram dois caminhos a mais para o mesmo
root, nenhum declarado; a F3 era o que chegava ao cluster sem passar pelo `docker`. Fechá-los é
condição **necessária** para que tirar o `aos` do grupo `docker` (decisão 2 do AOS-446) signifique
alguma coisa; não é suficiente.

**Alternativa rejeitada: correr a ponte como `User=aos`.** Dispensava o root por inteiro (a pasta é
do `aos` e o `aos` fala com o docker). Perde porque amarra a ponte ao grupo `docker`: no dia em que a
decisão 2 tirar o `aos` de lá, o reload do edge parava — em silêncio até à expiração.

### 6.3 F5 — o pino não é só `AOS_MANDATE_SIGNERS`

O `.env` do `aos` traz, lado a lado com a chave do humano, todas as âncoras que o nó confia:
`AOS_ISSUER_PUBKEY` (o emissor manual, **confiado por inteiro**: trocá-lo por uma chave própria
cunha qualquer raiz humana sem mandato nenhum), `AOS_OPERATORS` (`steer`/`pause`, `autonomy:set`,
`dsar:erase`), `AOS_RATIFIERS`, `AOS_POLICY_TRUST_ANCHOR` (a âncora do bundle PDP: trocar a âncora e
o bundle troca a política) e `AOS_WORM_TRUST_ANCHOR`; e o `secrets/approvers.json` do *four-eyes*,
também do `aos`. Proteger só a variável do mandato não fecharia nada — quem escreve o `.env` não
precisa dela. A Fase 1 regista **todas** estas âncoras no WORM, não uma.

### 6.4 Custódia — passos do dono, sem código

Decididos em 2026-09-26 e descritos em `deploy/server/README.md` §Custódia das chaves humanas:

1. A `humano-mandato.key` e a `issuer.key` saem da máquina que sela diariamente para suporte
   **offline cifrado**. Nenhuma das duas é usada pelas tarefas diárias (a selagem usa a
   `wormseal.key`; a cunhagem diária é o emissor automático); o custo é ir buscá-las para renovar um
   mandato (≤ 90 dias) ou cunhar à mão.
2. A chave SSH **interactiva** do operador para o `aos` passa a `ed25519-sk` (FIDO2): muda só o
   `authorized_keys`. A chave de deploy e as chaves de comando forçado (recolha dos backups,
   selagem) correm sem pessoa e ficam como estão.
3. **Declarado:** as duas seeds de aprovador do *four-eyes* vivem na mesma máquina. São duas
   chaves e **um** custodiante — o *four-eyes* prova duas assinaturas, não duas pessoas (adjacente
   ao DEF-107).

### 6.5 O que ficou para a Fase 1 — **entregue, ver §8**

Registar no WORM as âncoras da §6.3 (um pino trocado passa a deixar rasto selado) e assinar o
mandato com FIDO2 `sk-ssh` em vez da seed em claro. Foi numa onda seguinte porque mexe em
`platform/audit/record.go` e `platform/identity/mandate.go`, que o AOS-439 também mudou. **A
decisão está na §8.**

## 7. Emenda (AOS-439, 2026-09-27) — o mandato nomeia por quem o emissor age

Aditiva à §2.1. A decisão e o vínculo que a tornam imponível estão no **ADR-035**; aqui fica só o
que muda no mandato.

### 7.1 O campo `requesters`

O mandato passa a enumerar os **submissores** por quem o emissor pode agir: o `sub` do ID-token de
cada um, exactamente como o nó o grava no `planrequest.submitted`. É **obrigatório** na assinatura
(`SignMandate` e `aos-issuer mandate-sign --requesters` recusam sem ele), sem curingas, vírgulas,
espaços nem repetidos, com um tecto de 64 entradas. Um service account só submete se estiver nomeado.

Assina-se sob um domínio **novo**, `aos.identity.mandate.v2`, com a lista ORDENADA no fim do
`SigningInput`. Um mandato sem `requesters` produz exactamente os bytes do v1 — o mandato em vigor
continua a verificar —, e nenhum dos dois se converte no outro sem a chave do humano: arrancar os
`requesters` a um v2 pede o domínio v1, cuja assinatura o humano nunca produziu para aquele
conteúdo.

O nó recusa o `POST /runs` (e a retoma) de um run cujo submissor — o `requested_by`, que o nó
**deriva** da reclamação do pedido e nunca aceita do corpo (ADR-035 §2.2) — não conste dos
`requesters` (`E_MANDATE_REQUESTER`); e recusa, sob um v2, um run sem submissor.

### 7.2 A janela de migração dos v1

Um mandato v1 é aceite até `AOS_MANDATE_V1_UNTIL` e recusado depois (`E_MANDATE_V1_CLOSED`), pelo
relógio do nó. Fechada por omissão (fail-closed); no compose de produção a omissão é o fim do
mandato v1 em vigor (2026-10-25), para que o deploy não pare a drenagem. Tecto: 90 dias do arranque.
O banner declara a janela. Enquanto está aberta, o resíduo 7 (§5) vale para o mandato v1 — e um v1
continua a verificar mesmo depois de assinado o v2: o humano revoga-o (`revoke-sign --jti
mandate:<id>`) ou fecha a janela.

### 7.3 O que isto NÃO muda

- A raiz da cadeia on-behalf-of continua a ser o humano do mandato (ADR-003).
- Os limites da §2.1 (humano, agente, classe, política, board, escopo, TTL, janela) e a §6.
- A Fase 1 do AOS-446 (âncoras no WORM, mandato FIDO2) **está feita** (§8) e **preserva** o
  domínio v2: o campo `fmt` que ela introduz vive no ENVELOPE (`SignedMandate`) e não no
  `SigningInput`, pelo que os bytes que o v1 e o v2 assinaram são exactamente os de antes.


## 8. Emenda (AOS-446 fase 1, 2026-09-27) — a troca de uma âncora deixa rasto, e o mandato pode ser de hardware

Responde à §6.5. São **duas** decisões, e a segunda só vale porque a primeira existe: pôr a chave
do humano em hardware não serve de nada se trocar o *pino* no `.env` continuar a ser invisível.

### 8.1 As âncoras de confiança ficam seladas no arranque

O nó passa a selar, em **cada arranque** e na partição `trust-anchors` do WORM, a **impressão
digital** de cada âncora da §6.3: o emissor manual (`AOS_ISSUER_PUBKEY`), o emissor mandatado, os
assinantes de mandatos (`AOS_MANDATE_SIGNERS`), os operadores, os ratificadores, os aprovadores do
*four-eyes* (com a **autoridade** de cada um, não só a chave), a âncora da política e a do selador
do WORM. Uma âncora **ausente** é selada como ausente — é um facto sobre a postura, e omiti-la
tornaria «deixou de haver» indistinguível de «esta versão ainda não a conhecia».

**Sela-se sempre**, mude ou não, e o tipo do registo distingue as duas leituras
(`trust_anchors.changed` / `trust_anchors.active`). É o argumento **S-02** do changelog de política
(§ `policy_changelog.go`), e vale aqui letra por letra: decidir escrever a partir do que se lê da
própria partição dá a quem escreve no ficheiro um **botão de silenciamento** — pré-plantar um
registo com as impressões que vai instalar faz o nó concluir «igual ao último» e a troca real nunca
é registada.

**Impressões, e não chaves.** Uma pubkey é pública, mas pô-la no registo faria do WORM um
directório de chaves e fá-lo-ia crescer com o número de operadores. O que ele precisa de provar é
«mudou / não mudou». A impressão de uma chave FIDO2 é a **do `ssh-keygen -lf`** (`SHA256:…`), de
propósito: o operador confere sem converter nada.

**E as JANELAS, que também são autoridade** (achado A3 da revisão adversarial, 2026-09-27). Sob um
mandato v1 o emissor age por **qualquer** submissor; com dois pinos em vigor, **duas** chaves
assinam mandatos. Root que estenda `AOS_MANDATE_V1_UNTIL` ou `AOS_MANDATE_DUAL_PIN_UNTIL` alarga o
que o nó aceita **sem tocar em chave nenhuma** — e, até à revisão, o digest ficava byte a byte
igual. Os dois instantes entram no retrato como valores; o digest cobre-os de graça.

**O que NÃO entra, e é decisão:** o piso de frescura (`AOS_WORM_EXPECTED_HEAD(S_FILE)`) e o
ficheiro de checkpoints. A `AOS_WORM_TRUST_ANCHOR` entra porque é uma **chave**; o piso e os
checkpoints são o **estado** da ancoragem e mudam legitimamente a cada selagem diária. Selá-los
faria o registo declarar uma «troca de âncora» todos os dias — e um sinal que dispara todos os
dias é um sinal que ninguém lê, o que destruiria este registo para cobrir um vector que já está
fechado noutro sítio: baixar o piso é recusado pelo `VerifyFromCheckpointAtHead` no arranque
(`ErrCheckpointStale`).

**A âncora da política é a do PDP composto** (achado A4), e não `AOS_POLICY_TRUST_ANCHOR`: um
bundle aberto sem `WithTrustAnchor` tira a âncora do próprio directório, e aí a variável não é a
chave que verificou o bundle. Sai de `pdp.PDP.TrustAnchor()`.

### 8.2 E quem as verifica corre **fora** do host

Selar no WORM, sozinho, não vale muito: quem escreve o `.env` também escreve o ficheiro do WORM, e
o `EntryHash` é um SHA-256 **sem chave**. O que fecha é o `aos-issuer worm-seal` — que corre na
máquina do operador, com uma chave que não está no host — comparar, **entre âncoras diárias**, o
retrato que a selagem anterior cobria com o que está no WORM agora. Diferentes ⇒ **recusa selar**
(`ErrWormSealAncorasTrocadas`), nomeando as âncoras que mudaram. Uma rotação legítima aceita-se com
`--aceitar-ancoras <digest>`: exige-se o **digest exacto**, e não um `--sim`, porque escrever o
valor obriga a olhar para ele.

**VARRE TODOS OS REGISTOS DO INTERVALO, e não só o último de cada lado** (achado A1 da revisão
adversarial de segurança, 2026-09-27, **provado a correr**). A primeira versão fazia duas leituras
— o último registo ancorado e o último registo do store — e concluía sobre o intervalo inteiro. Os
registos **do meio** nunca eram lidos, e derrotá-la não exigia apagar nada:

1. root troca o pino e reinicia; o nó sela **honestamente** um `trust_anchors.changed` (seq N+1);
2. root **deixa a troca em vigor** e faz *append* de um `trust_anchors.active` com os parâmetros
   **antigos** (seq N+2) — escrever no ficheiro do WORM é o que ele já podia fazer;
3. a selagem seguinte lia o primeiro e o terceiro, dizia «INALTERADAS» e **selava** (exit 0).

Medido exactamente assim, com a sequência `[honesto, do-atacante, honesto]`. A varredura fecha-o
porque o registo do passo 1 fica no caminho: a troca real deixou rasto, e o rasto passa a ser
**lido**, com o `audit_seq` nomeado na recusa. Para o esconder, o atacante tem agora de **apagar**
o registo N+1 — e aí caem as guardas que já existiam (`ErrWormSealRecuo` / `ErrWormSealDivergencia`),
porque o `audit_seq` é *gapless* dentro da partição.

**O que isto NÃO fecha, e é preciso dizê-lo com as mesmas letras:**

1. **O tail ainda não ancorado.** Tudo o que está depois do último checkpoint é reescrevível por
   inteiro por quem tem root — o `EntryHash` é um SHA-256 **sem chave**, pelo que apagar os
   registos do intervalo e re-encadear o que resta produz um WORM que verifica. A garantia vale
   para registos que **já atravessaram uma selagem**; para os outros, o que se ganha é obrigar o
   atacante a **reescrever** em vez de acrescentar.
2. **A janela entre a troca e a selagem seguinte**, se a troca, a reposição **e** a reescrita do
   tail couberem toda dentro dela. Encolher a janela é selar mais vezes; fechá-la exigiria uma
   testemunha independente — **DEF-268**.
3. **O primeiro registo de sempre**: até haver uma selagem que o cubra não há base de comparação,
   e o banner do arranque di-lo.

### 8.3 E a chave do mandato entra no selo de **cada decisão** — `SchemaV5`

O `mandate_id` que a onda do AOS-439 selou (§7, resíduo 3) **não** denuncia uma troca de pino: o id
é escolhido por quem assina, e quem trocar `AOS_MANDATE_SIGNERS` pela sua chave re-assina um mandato
com o mesmo nome de humano e o mesmo id. O que muda é a **chave que o aceitou**, e é a impressão
dela — `mandate_signer` — que passa a ser selada, no `SchemaV5`.

**Porquê uma época nova e não o campo dentro do `SchemaV4`.** O v4 ainda não tem registos em
produção, e a tentação era poupar uma época. Perde por uma razão concreta: o binário que **escreve**
v4 já está entregue (a release da onda B1 corre hoje em produção) e basta o operador pôr
`AOS_AUDIT_WRITE_V4=1` para nascerem registos v4 com a serialização **dessa** release. Se o v4
passasse a significar outra coisa, esses registos deixavam de verificar — e como o arranque
re-verifica a hash-chain fail-closed, **o nó deixava de arrancar sobre o seu próprio log**. Uma
versão de formato só protege o que promete enquanto significar **um** layout de bytes, para sempre.

O **expand/contract** do AOS-439 mantém-se tal e qual: esta release **lê e verifica** v3, v4 e v5, e
**escreve v3** por omissão. A época sobe com `AOS_AUDIT_WRITE_SCHEMA=4|5`, passo do operador.
`AOS_AUDIT_WRITE_V4=1` continua a funcionar como sinónimo de `=4` — está no `docker-compose.prod.yml`
e tirá-la faria um deploy que a tivesse ligada passar, em silêncio, a escrever v3; as duas em
**desacordo abortam** o arranque, porque quando a configuração se contradiz nenhuma das leituras é
a intenção do operador.

A **retoma** passa a comparar também a impressão (`resume.go`), a par do humano e do mandato. A
consequência é a gémea da que o AOS-439 declarou: **rodar a chave do humano torna os runs suspensos
sob a anterior irretomáveis**. É o preço de a retoma exigir a mesma autoridade que autorizou o run.

### 8.4 O mandato pode ser assinado por uma chave FIDO2 `sk-ssh-ed25519`

A seed em ficheiro da §6.1 — hex, em claro, sem passphrase — copia-se com um `cat`, e quem a copie
assina mandatos para sempre sem o humano dar por isso. Uma chave FIDO2 residente **não se copia**, e
a assinatura só existe se alguém tocar no dispositivo.

**A verificação é em stdlib** (`platform/identity/sshsig.go`): SSHSIG (PROTOCOL.sshsig) sobre
`sk-ssh-ed25519@openssh.com`, na namespace própria `aos.identity.mandate`, com o
`SHA-256(application) || flags || counter || SHA-256(signed-data)` que o OpenSSH assina. Sem
dependências novas — o ambiente de build é offline —, e o formato **não foi lido de memória**: os
vectores de `platform/identity/testdata/aos446_sshsig_sk_vectores.json` foram gerados com esta
serialização e **aceites pelo `ssh-keygen -Y verify` do OpenSSH_10.3p1**, que os recusa com o
contador mutado, a namespace trocada ou a mensagem trocada.

**A `application` é exigida, e não só não-vazia** (achado A5). O pino tem de trazer
`application=ssh:aos-mandate`. Uma chave gerada sem `-O application=` fica com `ssh:` — e é
exactamente o que o §6.4 manda criar **na mesma máquina** para o SSH interactivo do operador.
Pinada como assinante de mandatos, cada login passaria a produzir assinaturas sob a mesma
`application`, e a namespace SSHSIG seria a única coisa a separar os dois usos; exigir a
`application` põe uma segunda separação, e é a que o operador vê no pino.

**Somos mais estritos do que o OpenSSH num ponto, e é deliberado.** Medido, não suposto: um vector
com `flags = 0x00` — **nenhuma presença de utilizador** — é aceite pelo `ssh-keygen -Y verify` sem
uma palavra. Para um mandato isso é o contrário de tudo o que ele existe para provar, e o nó
**recusa-o** (`ErrSSHSigSemToque`).

**Assinar continua a ser fora daqui.** Falar CTAP2/USB-HID exigiria um driver que este binário não
tem. A cerimónia parte-se em dois comandos, com o toque no meio:
`aos-issuer mandate-prepare` (escreve o rascunho e os **bytes exactos** a assinar) →
`ssh-keygen -Y sign -n aos.identity.mandate` → `aos-issuer mandate-attach` (verifica contra o pino e
emite o mandato com `fmt: sshsig`).

### 8.5 O que acontece a cada combinação de pino e formato

O `fmt` **não entra** no `SigningInput` — se entrasse, os bytes de todos os mandatos já assinados
mudavam, e nem o **v1 que corre hoje em produção** (dentro da janela `AOS_MANDATE_V1_UNTIL`, aberta
até 2026-10-25) nem o **v2 acabado de entrar** voltavam a verificar. Vive no envelope
(`SignedMandate`), e **quem decide é o pino**, que está no `.env` do nó e não viaja com o documento:

| pino em `AOS_MANDATE_SIGNERS` | `fmt` ausente ou `ed25519` | `fmt: sshsig` |
|---|---|---|
| 64 hex (software) | **aceita** — o caminho de sempre, v1 e v2 inalterados | **RECUSA**: o pino não é hardware |
| `sk-ssh-ed25519@openssh.com …` | **RECUSA**: um pino de hardware não aceita assinatura crua | **aceita**, com presença de utilizador obrigatória |

As duas recusas são o que torna o campo **inofensivo**: trocá-lo no documento não converte uma
assinatura de software numa de hardware nem o contrário. E um mandato FIDO2 é **distinguível** — pelo
`fmt`, pelo pino e pela impressão `SHA256:…` que entra no selo de cada decisão.

O valor **não se apara** (achado A5): `" sshsig "` era aceite. É uma enumeração fechada de dois
valores escritos por uma ferramenta, não texto de um humano — qualquer outra coisa é recusada.

**Compatibilidade, caso a caso:** um mandato de software continua a serializar **sem** o campo
`fmt` (o `SignMandate` deixa-o vazio de propósito), pelo que um binário anterior — que descodifica
com `DisallowUnknownFields` — continua a lê-lo. Um mandato FIDO2 apresentado a um binário anterior
ou é recusado na descodificação, ou é lido como ed25519 cru sobre um envelope SSHSIG e não verifica:
as duas saídas são fail-closed. Um mandato FIDO2 só funciona depois de o nó **e** o emissor
subirem **e** o pino ser trocado.

### 8.6 Um parser de assinante, e não dois

A gramática de `AOS_MANDATE_SIGNERS` tinha dois leitores: o do nó (`parseMandateSigners`, que impunha
a forma inteira) e o do emissor (`pubkeyDoHumano`, que devolvia texto e não impunha nada). Enquanto o
pino era sempre hex a divergência era invisível; com duas formas de pino deixaria de ser. Passa a
haver um, em `platform/identity` (`ParseMandateSigners` → `MandateSigner`), e um `.env` que o nó
recusa deixa de poder cunhar no emissor.

### 8.7 O que a fase 1 **não** muda

- A §6.1 continua igual: enquanto o `aos` estiver no grupo `docker`, o conjunto de quem contorna o
  mandato é o mesmo. A fase 1 não impede a troca — **denuncia-a**.
- A decisão 2 do AOS-446 (tirar o `aos` do grupo `docker`) continua **aberta**.
- O **contador** de uma assinatura SSHSIG aceita qualquer valor, incluindo um recuo (como o
  `ssh-keygen -Y verify`). Detectar um autenticador clonado exigiria guardar o último valor por
  chave, que é estado persistente que este verificador não tem — e o impacto aqui é nulo: um
  mandato assina-se **uma** vez e verifica-se muitas sobre os **mesmos** bytes. Quem fecha o clone
  é a custódia do autenticador. Declarado na revisão adversarial (A5).
- O `worm-seal` de uma **release anterior** lê um WORM v4 como «hash-chain adulterada»; com o v5
  passa-se o mesmo, e pela mesma razão. O agravamento é de grau, não de natureza: quem subir a época
  fica preso a um selador desta release ou posterior.
### 8.8 A janela de rotação de pinos — dois pinos por humano, com data-limite

**Decisão do dono, 2026-09-27** (achado A7 da revisão adversarial). Trocar o pino de um humano do
software para o FIDO2 invalida, **no mesmo instante**, todos os mandatos dele: a verificação da
assinatura corre antes da janela dos v1 e antes de tudo o resto. Entre reescrever o `.env` e
entregar o mandato novo, a drenagem **pára**. As opções eram a paragem programada ou a janela; o
dono escolheu a janela.

`AOS_MANDATE_SIGNERS` passa a admitir **dois** pinos para o mesmo `user_id`, e
`AOS_MANDATE_DUAL_PIN_UNTIL` (RFC 3339, molde de `AOS_MANDATE_V1_UNTIL`) diz até quando. Os dois
pinos verificam, e o `mandate_signer` de cada decisão selada diz **qual** — que é como o operador
confirma, pelo WORM, que o mandato em uso já é o de hardware antes de remover o antigo.

**Fail-closed em todo o lado:** vazia com dois pinos ⇒ o arranque **aborta**; já passada com dois
pinos ⇒ **aborta**; mais de dois pinos para o mesmo humano ⇒ **aborta** (uma rotação é **uma**
troca, não um conjunto de chaves que ninguém sabe justificar); o **mesmo** pino repetido ⇒
**aborta** (não é uma rotação); a mesma chave sob **dois nomes** continua a abortar, como sempre —
é a única forma de repetição que destrói a atribuição. Se a data passar com o nó a correr, um
mandato desse humano é recusado com `E_MANDATE_DUAL_PIN_CLOSED`.

**Não se escolhe um dos dois no fim da janela**, e é deliberado: escolher seria decidir a
autoridade do humano por conta própria, e nenhuma das escolhas é evidente (o mais recente no
`.env` não é necessariamente o novo). Recusar força o operador a dizê-lo — que é o passo que a
janela existe para lhe lembrar.

O **emissor** tenta os dois pelo mesmo motivo: se escolhesse um, recusaria com «assinatura
inválida» o mandato que o nó aceitaria. E a tabela de §8.5 continua a valer **por pino**: o pino
de software só aceita `ed25519`, o de hardware só aceita `sshsig`, cada um por si.

**A RETOMA TAMBÉM COMPARA O PINO, e isso interage com a rotação.** A guarda de `resume.go`
confronta o `mandate_signer` gravado no registo de retoma com o da credencial fresca: um run
suspenso sob o pino antigo **não retoma** sob o novo. Durante a janela isso é o comportamento
certo (a autoridade que autorizou o run é a que o continua), mas quer dizer que **fechar a janela
com runs suspensos torna-os irretomáveis**. Ou se esperam os runs, ou se aceita perdê-los.
E há um caso em que a guarda **não dispara** — ver §8.9.

### 8.9 Emenda (2.ª ronda de revisão, 2026-09-27)

**B1 — `--aceitar-ancoras` é uma LISTA.** O flag guardava um digest e comparava com `==`, e o
procedimento de rotação produz **duas** mudanças do retrato: o passo que abre a janela e o que a
fecha. Com as duas entre dois selos diários, **nenhuma** forma de invocar o flag selava — e a
mensagem mandava declarar «o digest de cada retrato que reconhece», que era impossível de
cumprir. A saída que isso empurrava era largar o `--anterior`, e sem ele a verificação da §8.2
**nem corre**. Passa a aceitar uma lista separada por vírgulas, comparada por pertença, e o
diagnóstico diz quais dos declarados foram usados e quais não apareceram. O procedimento também
passa a dizer que se pode selar **entre** os dois passos.

**B2 — a época de escrita decide se a rotação é conferível.** O passo de confirmação manda ler
`signer=SHA256:…` no `audit-trail`, mas o `mandate_signer` só entra no selo a partir do **v5** — o
`stampSchema` apaga-o abaixo disso — e produção escreve **v3** por omissão. O operador fazia o
grep, não via nada, e fechava a janela às cegas. Por isso `AOS_AUDIT_WRITE_SCHEMA=5` passa a ser
**pré-requisito** de abrir a janela (e o aviso do corte de rollback mudou-se para lá), e o banner
declara-o quando a época é inferior. **Consequência gémea, declarada:** com v3/v4 a guarda da
retoma compara um `rec.Principal.MandateSigner` **vazio** e não dispara — hoje, em produção, um
run suspenso sob o pino A retomaria sob o B sem dizer nada.

**B3 — a colisão de chave entre FORMAS.** A guarda de «a mesma chave sob dois nomes» indexava
pela impressão, que é `ed25519:…` num pino de software e `SHA256:…` num FIDO2: a **mesma** chave
ed25519 nas duas formas passava, e quem a detivesse assinava mandatos em nome dos dois humanos.
Passa a colidir também pela chave **crua**. Sob o mesmo humano continua a passar — o detentor é o
mesmo, e não acrescenta ninguém à autoridade.

**B4 — as duas janelas ao mesmo tempo.** Um mandato **v1** assinado pelo pino acabado de
acrescentar é aceite, e sob um v1 o emissor age por **qualquer** submissor: durante a rotação
abre-se um caminho para contornar os `requesters` (§7.1). **Não se recusa** — em produção o
mandato vivo *é* v1 e recusar partiria a rotação no estado actual —, mas o procedimento manda
**fechar a janela dos v1 primeiro** (re-assinar com `--requesters` e pôr `AOS_MANDATE_V1_UNTIL` no
passado) e o banner avisa quando as duas estão abertas.
