# ADR-033 — O emissor automático vive no servidor, e é o NÓ que o limita: o mandato

- **Estado:** Aceite — §2.1, §3 e §5 **emendados** (2026-09-26, AOS-446): quem contorna o
  mandato não é só root no host, e a ponte TLS dava root a quem escrevesse num ficheiro do `aos`
  (§6)
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
3. **O `Principal.MandateID` não é ainda selado nos registos de decisão.** O verificador devolve-o;
   a atribuição «este run correu sob o mandato X» fica para quando a auditoria o consumir.
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

### 6.5 O que fica para a Fase 1

Registar no WORM as âncoras da §6.3 (um pino trocado passa a deixar rasto selado) e assinar o
mandato com FIDO2 `sk-ssh` em vez da seed em claro. Vai numa onda seguinte porque mexe em
`platform/audit/record.go` e `platform/identity/mandate.go`, que o AOS-439 também muda.
