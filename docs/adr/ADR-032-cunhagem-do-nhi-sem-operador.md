# ADR-032 — A cunhagem do NHI sem operador: as quatro decisões, e o que fica por construir

- **Estado:** Aceite (decisões); implementação **parcial** — ver §5
- **Data:** 2026-09-24
- **Ticket:** AOS-427
- **Relacionados:** ADR-003 (cadeia `on-behalf-of` com raiz humana), ADR-006 (credential broker JIT;
  invariante 2 «TTL curto»), ADR-016 (§1, assinar em nome do humano exige hardware do humano),
  ADR-027 (a validade do NHI é o tecto de duração de um plano)

## 1. Contexto

O caminho do plano está completo desde o AOS-423: um objectivo entra por `POST /plans`, o
`aos-orq consume` reclama-o e corre-o. **Falta a credencial.**

O NHI do run é cunhado à mão: dois logins no browser (audiências `aos-issuer` e `aos-node`), com
a `issuer.key` na máquina do operador, e o resultado copiado para o servidor. Não há script,
`cron` nem `systemd timer` no repositório que o produza ou renove — `deploy/server/systemd/` tem
apenas o `aos-tls-sync`. É procedimento manual não versionado, e é a última coisa entre o estado
de hoje e «usável sem operador».

### As quatro portas que ficam fechadas

Não se reabrem sem ADR de supersessão:

1. **A `issuer.key` não vai para o servidor.** O `docker-compose.prod.yml` é explícito: tornar
   `AOS_ISSUER_KEY_PATH` definível «é oferecer a porta que a postura fecha». Se a privada vivesse
   no servidor, quem o comprometesse mintaria a sua própria identidade.
2. **O nó não confia em `iss:aos-orq`.** O orquestrador já cunha identidades em runtime com um
   emissor efémero por processo, mas essa confiança é auto-referencial e confinada ao seu Model
   Gateway. O ADR-027 §2.2 rejeita estendê-la ao nó.
3. **Não se assina em nome do humano sem hardware do humano** (ADR-006 invariante 6, ADR-016 §1).
4. **O `integration.IssuerAuthority` não se compõe no nó.** Tem `MintForAssertion`, mas só é
   composto no ramo não-endurecido, e a produção proíbe esse ramo.

## 2. Decisões

### 2.1 A prova é uma delegação de longa duração, assinada UMA vez por um humano

Hoje a raiz da cadeia é um ID-token OIDC verificado, e o humano sai do `sub`. Sem browser não há
ID-token fresco.

**Mantém-se a raiz humana do ADR-003; muda a FRESCURA da prova.** Um humano assina uma vez um
documento que autoriza cunhagens dentro de limites, e o emissor verifica essa assinatura em vez
de um ID-token do momento.

Alternativa rejeitada: **`client_credentials` do próprio serviço.** É o caminho padrão e o mais
simples, e não tem `sub` humano — relaxaria a exigência de raiz humana do ADR-003, e passaria a
haver acto sem pessoa atrás. Custo de a aceitar: emenda ao ADR-003, não ADR novo.

Custo da decisão tomada: a assinatura do humano passa a ser um artefacto de alto valor, com
rotação e revogação próprias — e a revogação **não existe hoje**: o mecanismo actual é por `jti`
de *token*, não de *delegação*.

### 2.2 A autoridade de emissão é externa, com `crypto.Signer` sobre Vault

É o desenho que o ADR-006 pede, e de que o `aos-issuer --vault-addr` já é meia implementação: o
`vaultTransitSigner` está completo e testado, e a chave nunca entra no processo.

Alternativas rejeitadas: **o `aos-issuer` actual corrido por timer no servidor** (obriga a chave,
ou o token do Vault que lhe dá acesso, a viver no servidor — a porta que a §1.1 fecha); **um
serviço de cunhagem no nó** (dá ao nó o poder de cunhar identidades, que é o análogo exacto do
que o ADR-027 §2.2 rejeitou para o `aos-orq`).

Custo aceite: **um processo novo em produção**, com ciclo de vida, rede e problema de arranque
próprios.

### 2.3 Há renovação, com sensor obrigatório

O ADR-027 fixa que a validade do NHI é o tecto de duração de um plano. Com renovação, esse tecto
cai: bom para planos longos, e mau para o raio de acção de uma credencial comprometida.

O sensor não é acessório — é a condição. Uma credencial que caduca sem ser renovada tem de ser
visível **antes** de o run falhar, não depois.

### 2.4 O tecto máximo de TTL é imposto na BIBLIOTECA — **e é a parte já implementada**

`identity.TTLMaximo = 1 hora`, validado na construção do emissor, nas duas vias
(`NewIssuer` e `NewIssuerWithSigner`).

**Porquê na biblioteca:** vale para os três chamadores de hoje e para os que ainda não existem,
sem depender de nenhuma receita estar certa. **Porquê uma constante:** um tecto configurável é um
tecto que um deployment novo volta a poder levantar; a decisão foi tornar impossível, não
desencorajar. **Porquê recusar e não aparar:** um clamp faria o banner dizer um TTL e o token ter
outro, e um tecto que mente sobre si próprio é pior do que tecto nenhum.

**Porquê uma hora, medido:** o nó emite a 15m, o orquestrador a 30m, o CLI a 15m por omissão e a
receita de produção passa 45m. Uma hora fica acima de todos e continua a tornar impossível um NHI
que dure um turno.

Cobre também o **TTL zero ou negativo**, que nascia expirado e nunca tinha sido recusado — nem
testado.

## 3. Porque é que isto importava agora

O atrito da cunhagem manual era uma defesa **acidental**: dois logins por token limitam o raio de
acção sem que ninguém o tenha decidido. O AOS-427 remove esse atrito — e uma defesa acidental
desaparece exactamente no momento em que a emissão passa a ser automática, que é o pior momento
possível, porque ninguém a vê sair.

Por isso o tecto entra **antes** do resto, e não depois.

## 4. Dois achados que condicionam o que falta

**O `mint` não audita.** O caminho CLI não passa `WithEventStore`, logo `recordIssued` é no-op e
não existe evento `identity.nhi.issued`. O `Issue` já é fail-closed quando o store existe e falha
— falta ligá-lo. Uma cunhagem **automática** sem auditoria seria muito pior do que a manual sem
auditoria.

**O anti-replay está desligado de propósito.** O `mint` não liga `RequireJTI` porque o binário é
efémero e o armazém nasceria vazio a cada invocação. Um emissor persistente pode e deve ligá-lo;
não o fazer seria regressão.

## 5. O que fica por construir, e porquê

Este ADR regista quatro decisões. **Só a §2.4 está implementada.** As outras três estão bloqueadas
em desenho, e o desenho tem perguntas que não são de código:

1. **Onde corre o emissor externo, em concreto** — serviço no compose, ou o `aos-issuer` a ganhar
   um modo `serve`? Que rede alcança o Vault? O `aos-orq` pede o token (pull) ou o emissor escreve
   o ficheiro (push)? **Isto determina praticamente tudo o resto**, incluindo onde vive o sensor.
2. **O formato da delegação não existe** — não há tipo, ficheiro, esquema nem nome. Em aberto: que
   campos a assinatura cobre; se é curinga (o emissor cunha o que quiser dentro dos limites) ou
   enumera cada cunhagem; **como se revoga**; e qual é a validade da própria delegação, e quem a
   renova — que é o mesmo problema um nível acima.
3. **De onde vem a chave que assina a delegação.** O molde do repositório (`plan-approve-sign`) lê
   uma seed de ficheiro, o que contraria «sem hardware do humano» na prática ainda que não na
   forma.
4. **Onde vive o sensor.** O `aos-orq` **não expõe `/metrics`** — quem quer que passe a cunhar
   tem de criar a superfície, não apenas a série. O critério diz «visível antes de o run falhar»,
   o que exclui pô-lo no nó, que só sabe da credencial quando ela chega.
5. **A verificação em produção** exige, além de tudo isto, **alguém a drenar a fila**: o AOS-430
   mediu que nada o faz hoje (`profiles: ["orq"]`, `restart: "no"`, e o `consume` drena uma vez e
   termina).

O molde para (2) que o próprio repositório já registou está em
`packages/platform/identity/delegation/README.md`: dar a cada principal a sua chave, com cada elo
assinado pela chave do respectivo `Sub`. Está declarado fora de âmbito do ADR-006, e é a direcção.
