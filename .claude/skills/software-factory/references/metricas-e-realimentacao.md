# Métricas e realimentação

Lê este ficheiro quando fechares uma linha e for preciso converter o que aprendeste em portão.

- [Seis indicadores](#seis-indicadores)
- [Um escape vira portão](#um-escape-vira-portão)
- [O que faz de um portão um portão](#o-que-faz-de-um-portão-um-portão)
- [Padrões de escape neste repositório](#padrões-de-escape-neste-repositório)

---

## Seis indicadores

Uma fábrica que não se mede não melhora — repete. Nenhum destes indicadores precisa de
ferramenta nova: todos se leem de artefactos que já existem.

| Indicador | Onde se lê | O que diz |
|---|---|---|
| **Verde à primeira** | `gh pr checks` do PR, primeira corrida | quanto do meu trabalho chega pronto |
| **Taxa de retrabalho** | achados que entraram **depois** de eu dizer feito | se o meu «feito» significa alguma coisa |
| **Escapes por portão** | defeito encontrado a jusante do portão que o devia apanhar | onde a rede tem buraco |
| **Calibração** | achados `CONFIRMADO` / achados levantados | se a revisão procura ou se inventa |
| **Etapas saltadas** | `AOS_SKIPPED_STEP` no veredicto; exit `3` | garantias que não tenho |
| **Tempo até merge** | abertura do PR → squash | onde está a fila real |

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
