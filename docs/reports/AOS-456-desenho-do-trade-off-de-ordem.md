# AOS-456 — o trade-off de ordem na admissão por-chamador

| Campo | Valor |
|---|---|
| Data | 2026-09-27 |
| Estatuto | **Desenho proposto.** Não é decisão: não altera ADR nenhum e não autoriza implementação |
| Origem | A tentativa 1 do AOS-456 foi implementada, passou 31 gates de CI e foi revertida em `125396a` por não entregar o seu critério em nenhuma configuração |
| Pedido | Desenhar o trade-off que a reversão deixou por resolver |
| Decisão que fica ao dono | Qual das opções, e se a divisão do ticket proposta na §6 se aceita |

---

## 1. A restrição, dita com precisão

O critério do AOS-456 é: **a rajada de A não produz `429` em B**.

Para isso, os pedidos de A não podem consumir um recurso **partilhado** com B. Logo o consumo tem
de ser **imputado a A** — e imputar exige saber **quem é A**, o que exige **verificar** a sua
credencial.

Mas verificar custa trabalho. E o trabalho gasto a verificar é, ele próprio, um recurso partilhado.

> **A restrição irredutível:** existe uma janela em que um chamador que apresenta credencial
> consome CPU partilhado **antes** de ser atribuível, e nenhuma ordenação a elimina. É a mesma forma
> do DoS de handshake TLS, e a literatura não tem solução completa a este nível — tem mitigações.

A tentativa 1 falhou por ignorar isto: pôs o balde global **antes** da 2.ª etapa, pelo que cada
pedido recusado pelo balde por-chamador já tinha gasto um token global. Eu conheci a alternativa —
identificar primeiro — e rejeitei-a por medo de um vector de CPU **que não medi**.

## 2. O que a medição diz, e corrige o meu medo

| Caminho | Custo medido | |
|---|---|---|
| **Sem Bearer** (`bearerToken`: um `Header.Get` + prefixo) | **41 ns** | `read_credential.go` devolve `ErrNoReadCredential` antes de qualquer cripto |
| `ed25519.Verify`, assinatura **válida** | **59,9 µs** | |
| `ed25519.Verify`, assinatura **inválida** | **59,9 µs** | **igual** — é constant-time por desenho |
| Razão verify / sem-Bearer | **1461x** | |

*(Medido em 200 000 e 10 000 iterações respectivamente, neste contentor. Um JWT real acresce
base64+JSON, da mesma ordem com JWKS em cache; sem cache acresce I/O de rede, ordens de magnitude
acima.)*

**Três consequências que mudam o desenho:**

1. **Rejeitar tráfego anónimo é praticamente grátis.** O meu receio de «verificação a taxa
   ilimitada» **não se aplica a quem não apresenta credencial** — o `verify` sai em 41 ns nesse
   caso. Identificar antes do balde global é seguro para o tráfego anónimo.
2. **A verificação inválida custa o mesmo que a válida.** Um atacante que envia lixo paga zero e
   faz o nó pagar 60 µs. A ~16 600 verificações/segundo por core, é um DoS real e **limitado**.
3. **É por isso que o orçamento de verificação tem de existir como conceito próprio** — separado do
   orçamento de admissão.

## 3. Opção A — duas etapas com orçamento de verificação

```
pedido
  │
  ├─ bearerToken(r) == ""  ──►  BALDE GLOBAL (anónimo)  ──►  decode  ──►  …
  │                             41 ns para recusar
  │
  └─ Bearer presente  ──►  ORÇAMENTO DE VERIFICAÇÃO (global)
                             limita cripto/segundo, não admissões
                                │
                                ├─ esgotado  ──►  429 (e nada foi verificado)
                                │
                                └─ authorize (60 µs)  ──►  BALDE DE A  ──►  decode  ──►  …
```

**O que entrega:** o tráfego **admitido** de A não toca em nada de B. A rajada de A esgota o balde
de A, e B passa.

**O que NÃO entrega, e é o residual que não se pode esconder:** os pedidos de A **recusados pelo
próprio balde** ainda gastaram um token de **verificação**. A com 1000 pedidos/s de tokens válidos
esgota o orçamento de verificação e B não chega a ser verificado. A starvation muda de recurso — de
admissão para verificação — e encolhe, mas não desaparece.

**Dimensionamento:** o orçamento de verificação é uma afirmação sobre **CPU**, não sobre justiça.
Sai de `n_cores × 16 600 × fracção_aceitável`, e o banner tem de o dizer com o número.

## 4. Opção B — cache de verificação (compõe com A; sozinha não resolve)

Guardar o resultado da verificação indexado pelo **hash do token**, com TTL ≤ expiração do token.

**Porque importa:** sem ela, um chamador legítimo de alta taxa queima o orçamento de verificação
tanto como um atacante — e o mecanismo pune quem se porta bem. Com ela, A reutilizando **um** token
paga verificação **uma vez** e depois são acertos de cache; o orçamento de verificação passa a ser
gasto sobretudo por **tokens novos**, que é o que se quer limitar.

**O que não resolve:** um atacante que **rode** tokens derrota a cache. Mas rodar tokens **válidos**
exige o IdP (limitado fora do nó) e rodar **inválidos** cai no orçamento de verificação. A cache
não é a barreira; é o que torna a barreira da Opção A suportável.

**Cuidados que a tornam trabalho a sério, não uma linha:**

- **nunca sobreviver à expiração do token** — uma cache que aceita um token expirado é uma
  revogação que não funciona;
- **limitada e com evicção** — é a mesma armadilha que a tentativa 1 criou na tabela de baldes, e
  esta é indexada por valor controlado pelo atacante;
- **indexada pelo hash, nunca pelo token** — o token é material sensível e não entra em estruturas
  de longa duração, pela mesma regra que o proíbe nos logs;
- **a revogação de NHI tem de a invalidar** — senão a cache passa a ser a janela em que uma
  credencial revogada continua a servir.

## 5. Opção C — mudar de recurso: justiça em CONCORRÊNCIA, não em taxa

**Esta é a opção que eu não considerei na tentativa 1, e é a mais barata e a mais correcta.**

O recurso que um run consome não é «um token por segundo» — é **um lugar em execução**. O nó já o
limita: `AOS_INGRESS_MAX_INFLIGHT` sobre `len(s.runs)` (`service.go:1276`). E cada run já carrega o
`goal.Principal` do submissor.

```
CONCORRÊNCIA por-chamador = contar em s.runs os runs cujo goal.Principal é A,
                            e recusar quando A já tem o seu tecto
```

**Porque é estruturalmente diferente das opções A e B:**

> Um pedido **recusado** não ocupa lugar nenhum. Um pedido recusado **gasta** um token.

É essa assimetria que faz desaparecer o problema de ordem. Consequências:

- **Não exige reordenação nenhuma.** A verificação já acontece antes de o run ser hospedado; a
  contagem lê estado que já existe.
- **Não há atribuição-antes-de-verificação:** quando o run é criado, a identidade já foi verificada
  pelo caminho normal, e o contador só olha para runs **já admitidos**.
- **A não pode ocupar todos os lugares**, que é a forma concreta de «A não esfomeia B» para o
  recurso que interessa. B encontra sempre lugar.
- **Custo:** uma contagem sobre `s.runs` sob o mutex que já existe. Nenhuma tabela nova, nenhuma
  evicção, nenhum knob que congele o ingresso.

**O que não entrega:** justiça em **taxa**. A pode continuar a bater no balde global e a produzir
`429` em B por rajada. O balde global passa a ter um só papel — **proteger CPU** — e pode ser
dimensionado com folga para esse papel, porque a justiça deixou de depender dele.

## 6. Recomendação: dividir o ticket

O AOS-456 pede uma coisa que, lida à letra, **não é completamente alcançável** neste ponto do
sistema. Mas 80% do valor é alcançável sem nada do que fez a tentativa 1 falhar.

| | O que faz | Ordem | Residual |
|---|---|---|---|
| **AOS-456a** — concorrência por-chamador (Opção C) | tecto de runs EM CURSO por principal | **nenhuma alteração** | não dá justiça em taxa |
| **AOS-456b** — taxa por-chamador (Opções A+B) | orçamento de verificação + balde por-chamador + cache | identidade **antes** do balde de dados | atacante que queime verificação degrada-a para todos |

**Recomendo fazer o 456a primeiro e sozinho**, e não por ser mais fácil: é o que dá a propriedade
que interessa sobre o recurso que interessa, e o seu critério de aceitação é **medível no handler**
sem construir nada que o possa mascarar — que foi exactamente como a tentativa 1 passou verde.

Se o 456b se fizer, a mitigação para o seu residual **não vive no nó**: limitar tráfego
pré-autenticação por IP é trabalho do `edge` (nginx), que já termina TLS e já está no caminho. Vale
decidir isso antes de escrever código no nó — pode tornar o 456b desnecessário.

## 6-bis. DECISÃO TOMADA (2026-09-28), e o que o desenho não tinha visto

Este documento pediu que a escolha fosse feita antes de código. Foi, e ficou assim:

- **AOS-456a (concorrência) — FEITO** e mergeado, depois de três revisões adversariais.
- **AOS-456b (taxa) — FECHADO como DECIDIDO-E-NÃO-FEITO.**

**O que este desenho não tinha visto, e inverte a sua §6.** O desenho leu o rácio
`recusar : verificar` como *o custo de atribuir taxa*. É também, e sobretudo, *o preço de admissão de
um vector novo*: hoje o `bucket.allow()` corre na primeira linha do `handleSubmit` e a primeira
`ed25519.Verify` ~150 linhas depois, pelo que **o balde limita quantas verificações um chamador não
autenticado pode forçar** — 64/s por omissão, ou ~0,34% de um core. A reordenação que o 456b exige
**remove esse limitador**, e o «orçamento de verificação» da Opção A existiria para fechar um buraco
que a própria mudança abriu.

Medido de novo no contentor da decisão (e não reciclando os números desta análise): verificar 52,7 µs,
recusar 30,2 ns, rácio **1742x**. E a justiça por-origem que o 456b queria **já existia no `edge`**:
`deploy/server/nginx.conf`, `limit_req_zone $binary_remote_addr rate=16r/s` com burst 32, em
produção — este desenho recomendou olhar para lá e não o fez.

A razão completa, a fronteira não-coberta (taxa por-**principal**) e o gate que prende a ordem estão
no ticket: `specs/EPIC-20_Prontidao_Agentica_Remediacao.md`, secção AOS-456b.

## 7. O que este desenho NÃO faz

- **Não decide.** Nenhuma destas opções está aprovada. O 456a é o que recomendo; a escolha é do dono.
- **Não implementa.** Não há código, e de propósito: a tentativa 1 provou que escrever primeiro e
  desenhar depois custa um dia e um revert.
- **Não mede a Opção C.** O custo da contagem por-chamador sobre `s.runs` é **`NÃO VERIFICADO`** —
  é O(n) sobre os runs em curso, com `n ≤ maxInFlight` (512 por omissão), sob um mutex que já é
  tomado. Parece irrelevante e não o medi. Quem implementar mede antes de afirmar, e o critério de
  aceitação do 456a deve exigir esse número.
- **Não resolve o `POST /plans`.** Continua a ser segunda porta a consumir o balde global sem
  atribuição, e qualquer das opções tem de dizer se o cobre ou não — «uma barreira que só metade das
  portas respeita não é uma barreira».

## 8. Como a tentativa 1 falhou, em uma frase

Escrevi no cabeçalho do ficheiro o desenho certo — *«pedido atribuível ⇒ balde desse principal, e
só dele»* — e a seguir escrevi código que consumia o balde global primeiro. **A prosa descrevia a
solução e o código fazia outra coisa**, e nenhum dos 31 gates de CI podia ver a diferença, porque os
meus testes exercitavam a estrutura de dados e nunca o `handleSubmit`.
