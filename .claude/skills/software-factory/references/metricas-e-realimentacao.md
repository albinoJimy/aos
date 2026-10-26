# Métricas e realimentação

Lê este ficheiro quando fechares uma linha e for preciso converter o que aprendeste em portão.

- [Seis indicadores](#seis-indicadores)
- [Um escape vira portão](#um-escape-vira-portão)
- [O que faz de um portão um portão](#o-que-faz-de-um-portão-um-portão)
- [Padrões de escape neste repositório](#padrões-de-escape-neste-repositório)

---

## Seis indicadores

Uma fábrica que não se mede não melhora — repete. Seis indicadores, e a coluna que importa é a
do instrumento: é ela que separa medição de intenção.

| Indicador | Onde se lê | Instrumento |
|---|---|---|
| **Verde à primeira** | `gh pr checks <n>`, primeira corrida | artefacto do GitHub |
| **Etapas saltadas** | `AOS_SKIPPED_STEP` no veredicto; exit `3` | veredicto dos gates |
| **Tempo até merge** | abertura do PR → squash | artefacto do GitHub |
| **Calibração** | achados `CONFIRMADO` / achados levantados | `sessoes.py medir` |
| **Retrabalho** | achados `CONFIRMADO` pós-feito, por entrega | `sessoes.py medir` |
| **Escapes por portão** | achados com o portão que os devia ter apanhado | `sessoes.py medir` |

### O ledger dos três últimos

Os três de baixo não se leem de nenhum artefacto que o repositório já produza. Têm um ledger
próprio, ao lado do estado das sessões e pela mesma razão — é observação do agora, partilhada
entre worktrees, e versioná-la tornava-a invisível entre ramos até ao merge.

```bash
python scripts/ci/sessoes.py medir achado --veredicto confirmado --onde revisao
python scripts/ci/sessoes.py medir achado --veredicto morto --onde revisao --nota "..."
python scripts/ci/sessoes.py medir achado --veredicto confirmado --onde pos-feito --portao test
python scripts/ci/sessoes.py medir entrega --pr 566
python scripts/ci/sessoes.py indicadores --desde 30
```

**Dois tipos de registo, e os três indicadores derivam deles** — não há um contador por
indicador, porque três contadores mantidos à mão discordam entre si, que é a classe de defeito
que o `scripts/ci/` existe para fechar. O `achado` leva o veredicto do vocabulário desta skill e
**onde** apareceu: `revisao` se foi apanhado antes de entregar, `pos-feito` se entrou depois de
alguém declarar feito. A `entrega` é o denominador do retrabalho — sem ela não há indicador, e
o `indicadores` diz **`sem amostras`** em vez de `0%`, porque uma razão sem denominador não é
zero, é desconhecida.

**Duas regras de leitura que a primeira utilização a sério deste ledger obrigou a fixar:**

- **O retrabalho não é uma percentagem.** O numerador não está contido no denominador: uma
  entrega pode trazer três achados a jusante. Lê-se **por entrega** (`1,0 por entrega  (1/1)`).
  Imprimi-lo como `%` dava «500%», que não quer dizer nada. Percentagem só onde o numerador é
  parte do denominador — é o caso da calibração, e só dela.
- **O retrabalho conta só o `CONFIRMADO`.** Um achado `MORTO` que aparece depois de alguém
  declarar feito não é retrabalho: é uma hipótese que morreu e não gerou trabalho nenhum.
  Contá-lo inflacionava o indicador para o lado que faz a fábrica parecer pior do que é — e na
  primeira medição real a diferença foi entre `5/1` e `1/1`.

**O que este instrumento não é.** Não é um gate, e não entra no `run.sh` — no runner não haveria
ledger que ler, e um gate que passa sempre é pior do que verificação nenhuma. Continua a
depender de alguém chamar o `medir`. O que muda é que o número **acumula e lê-se**: uma entrada
em falta vê-se como lacuna ao ler o ledger contra os PRs do dia, enquanto uma contagem na cabeça
de quem mediu desaparece no fim da sessão. Diz isto assim quando citares um indicador — e a
calibração de «dois terços morrem» continua a ser observação acumulada até o ledger ter amostras
que a sustentem.

Três notas sobre como se leem:

**A taxa de retrabalho é a que não mente.** O *throughput* sobe facilmente com agentes — é o
indicador que a indústria viu subir enquanto a estabilidade de entrega descia, e foi por isso
que o DORA de 2025 acrescentou o retrabalho como quinta métrica. Contar PRs ou linhas premeia
exactamente o comportamento que esta skill existe para travar.

**A calibração corta nos dois sentidos.** Perto de 100% de achados confirmados significa que a
revisão só procurou o óbvio. Perto de 0% significa que está a produzir ruído. Neste
repositório, cerca de **dois terços das hipóteses adversariais morrem** contra o canon — é essa
a ordem de grandeza saudável.

**Um verde parcial é um indicador, não um detalhe.** Cada etapa saltada é uma garantia em
falta; o veredicto redeclara-as todas de propósito. Registar não é impedir.

---

## Um escape vira portão

> **Um escape que um portão podia ter apanhado vira portão.** Não vira aviso, não vira nota de
> rodapé, não vira «ter cuidado da próxima vez».

É a tese do AOS aplicada à própria fábrica: tornar a falha arquitecturalmente impossível, não
apenas desencorajada. Uma nota depende de alguém se lembrar de a ler — e esquecer-se é
indistinguível do caso correcto, que é a definição do defeito que a nota pretendia evitar.

Classifica primeiro. Há três destinos legítimos, e a escolha não é de gosto:

```text
ESCAPE → classificar → PORTÃO NOVO | PORTÃO ALARGADO | PADRÃO NOMEADO
```

| Destino | Quando | Exemplo desta base de código |
|---|---|---|
| **Portão novo** | a classe de defeito é verificável mecanicamente e nenhum portão a cobre | `stream-names` — todo o `stream_id` tem de ser representável num subject NATS |
| **Portão alargado** | existe portão para a classe, mas a varredura não chegava ao sítio | `policy-taint` a exigir `context.taint != untrusted` em **cada** `permit` |
| **Padrão nomeado** | o defeito exige julgamento e não se reduz a um predicado | os padrões em [`revisao-adversarial.md`](revisao-adversarial.md) §Padrões de defeito |

**O terceiro destino é o último recurso, não o primeiro.** Escolher «padrão nomeado» porque é
mais rápido do que escrever o gate é precisamente a troca que esta regra proíbe. Um argumento
correcto contra a opção forte não é cobertura para não fazer nem a forte nem a fraca.

### Depois de fechar o escape, pergunta se a forma é partilhada

Um defeito de **forma** — um nome mal construído, um valor-zero que significa «sem barreiras»,
uma configuração que degrada em silêncio — raramente está num sítio só. A revisão que fechou a
superfície nova de um ticket deixou treze streams antigos com a mesma forma expostos, e isso só
se corrigiu muito depois.

```text
Fechei o defeito aqui.
Quantos outros sítios têm esta forma?
A verificação que acabei de escrever varre-os todos, ou só o meu?
```

---

## O que faz de um portão um portão

Escrever um gate que não impede nada é pior do que não o escrever: produz verde e consome
confiança. Quatro testes, e qualquer um que falhe desqualifica o portão.

**1. Não se recalcula das entradas de quem chama.** Se o veredicto se deriva do que o chamador
forneceu, não é portão — é um eco. Amarra-o ao artefacto selado, e nunca grave o facto terminal
antes de o verificar.

**2. Não fail-open onde não tem com que colidir.** Um gate que passa sempre no runner porque ali
não existe a condição que ele persegue é fail-open silencioso. Foi por isso que o `sessoes.py`
**não** é gate de CI.

**3. Não é allowlist escrita à mão.** Um detector que partilha o modo de falha do defeito que
persegue — esquecer uma entrada produz o mesmo silêncio que esquecer a chamada — não serve.
Deriva a lista do registo único por onde tudo passa obrigatoriamente.

**4. O controlo negativo tem de ter alvo.** «Não consegue X» testado sobre um recurso que não
existe passa com qualquer política. Pergunta à ACL e confirma que o alvo existe antes de
acreditar na negativa.

E quando acrescentares um portão, pergunta a pergunta que o repo já aprendeu a fazer: **como é
que a ausência dele se torna visível?** Uma verificação imposta por quem escreve se lembrar de a
chamar é uma barreira por convenção, e o esquecimento é indistinguível do caso correcto em dev,
em CI e em produção. O idioma usado aqui é o conjunto fechado — a tabela de rotas declara o
plano de cada rota e uma rota sem plano **aborta o arranque**.

---

## Padrões de escape neste repositório

Catálogo curto do que já passou por tudo. Procura-os por nome antes de declarar uma revisão
feita.

**Gates e smoke só correm sobre ficheiro.** Um defeito de forma que não esteja num ficheiro
exercitado passa por todos os portões, pela revisão adversarial e pelo smoke. Pergunta sempre
qual é a entrada que faria a verificação olhar para o teu caso.

**Validação dirigida não cobre o pacote.** `-run TestX|TestY` deixa passar testes do mesmo
pacote que só correm noutro gate. Verde dirigido não é verde.

**Medir uma vez apresenta não-determinismo como determinismo.** Uma corrida só não distingue
defeito de flake. A terceira corrida de uma medição já desmentiu um achado que estava escrito.

**A correcção pode ser pior do que o achado.** Rever o diff não é rever a correcção: trocar
fatal por não-fatal muda quem limpa o que ficou a meio. E escolher o guarda mais largo «por ser
fail-closed» já abriu uma regressão viva, onde o estreito já estava garantido a montante.

**Seguir o valor até quem o fornece.** Afirmar que uma defesa existe sem seguir a variável até
à origem já aconteceu três vezes — e a afirmação entra num documento, onde passa a ser a razão
pela qual ninguém volta a verificar.

**A negativa de um subagente é uma afirmação.** «Isso não existe no repo» era falso e decidia o
âmbito de uma fase inteira. Confirma com comando próprio.

**Medir causas, não nomes de teste.** «Trinta e nove testes, na maioria andaime» escondia
catorze defeitos vivos. Um aperto de contrato vale como instrumento de medição; uma contagem
não.
