# Revisão adversarial, evidência e regressão

Lê este ficheiro quando estiveres a rever — o teu trabalho ou o de outro agente.

- [O guião](#o-guião)
- [Evidência antes de conclusão](#evidência-antes-de-conclusão)
- [Regressão](#regressão)
- [Padrões de defeito neste repositório](#padrões-de-defeito-neste-repositório)

---

## O guião

A revisão procura razões para a solução estar **errada**. Se terminas a rever e não encontraste
nada, a pergunta seguinte não é "está bom?" — é "que classe de defeito é que eu não procurei?".

```text
Como é que esta implementação falha?

O que foi assumido sem evidência?

Que requisito do ticket pode ter ficado de fora?

Que cenário extremo quebra isto?

Que consumidor é afectado — e foi verificado, ou presumido?

Que regressão pode surgir daqui?

Que comportamento anterior mudou sem que ninguém tenha decidido mudá-lo?

O teste pode estar a passar pela razão errada?

Existe solução mais simples que preserve o comportamento?
```

**"O teste pode estar a passar pela razão errada?"** é a pergunta com melhor retorno. Formas
comuns de um teste verde não provar nada:

- o `-run` não casa nenhum teste e o `go test` sai 0 (é por isto que os gates deste repo usam
  `require_tests` com a lista de nomes obrigatórios);
- o teste exercita o mock e não o código;
- a asserção é sobre a ausência de erro, e o caminho que interessa nunca foi tocado;
- o setup já garante a pós-condição que a asserção verifica;
- o teste passa com a implementação **e** com a implementação anterior — não discrimina nada.

O último tem verificação directa: reverte a alteração, corre o teste, confirma que fica
vermelho. Um teste que passa nos dois estados não está a testar a alteração.

---

## Evidência antes de conclusão

Cada afirmação relevante precisa de evidência nomeada.

```text
AFIRMAÇÃO: "O endpoint mantém compatibilidade."
EVIDÊNCIA:
- TestAOS263_CLIDecisaoExigeOsCamposQueAmarram cobre o corpo que o cliente envia;
- tecnica/12_Contratos_de_Interface.md inalterado no diff;
- packages/cmd/aos-orq compila e `driver.sh smoke` passa.
```

Cita testes **pelo nome da função**, não por ficheiro e linha: os números de linha derivam a
cada edição e uma citação que aponta para o sítio errado é pior do que nenhuma.

```text
```

Substitui isto:

```text
"Provavelmente funciona."   "Deve estar correcto."   "Não parece haver problemas."
```

Por isto:

```text
"Validado através de X, Y e Z."       ou       "NÃO VERIFICADO."
```

`NÃO VERIFICADO` é informação. Diz ao leitor exactamente onde ele tem de olhar. Uma frase
tranquilizadora sem evidência remove essa informação e não acrescenta nenhuma — é estritamente
pior do que admitir a lacuna.

---

## Regressão

Toda a alteração tem três partes, e a segunda é a que ninguém verifica:

```text
COMPORTAMENTO NOVO  +  COMPORTAMENTO PRESERVADO  +  REGRESSÃO POSSÍVEL
```

Perguntas obrigatórias:

- O comportamento existente foi preservado — verificado, ou presumido?
- Algum consumidor quebra?
- Alguma interface mudou de assinatura, de semântica ou de código de erro?
- Alguma configuração mudou de significado, de default ou de obrigatoriedade?
- Algum fluxo indirecto foi afectado?
- Algum teste existente devia ter sido actualizado — e não foi porque continua a passar?

A última é a mais traiçoeira. Um teste que continua verde depois de o comportamento mudar não
é boa notícia: significa que não cobria o que se pensava que cobria.

---

## Padrões de defeito neste repositório

O AOS tem modos de falha característicos, quase todos silenciosos. Procura-os por nome:

**Barreira por convenção.** Uma verificação de segurança imposta por quem escreve se lembrar de
a chamar, e cujo esquecimento é indistinguível do caso correcto em dev, em CI e em produção. O
antídoto usado aqui é o conjunto fechado: a tabela de rotas em `packages/cmd/aos/planos.go`
declara o plano de cada rota, e uma rota sem plano **aborta o arranque**. Se estás a acrescentar
uma verificação, pergunta como é que a ausência dela se torna visível.

**Valor-zero válido.** Um campo cujo valor por omissão compila e passa nos testes, mas
representa "sem barreiras". O idioma do repo é fazer o valor-zero inválido
(`planoPorClassificar`, `ClassDanger = iota`, `SensitivityUnknown` a resolver para o topo).

**Degradação silenciosa de configuração.** Uma env var malformada que é ignorada em vez de
abortar. O repo trata isto como fail-closed de config (`ErrBadBoardRegions`,
`ErrBadPolicyTrustAnchor`, `ErrProgressBudgetUnwired`) — "não configurado" e "configurado e
inválido" têm de ter destinos diferentes.

**Metade composta.** Um subsistema que precisa de duas env vars e fica inerte com uma só —
`AOS_AUTONOMY_LEVELS` sem `AOS_POLICY_BUNDLE_DIR` é ignorada em silêncio. Se acrescentares
composição condicional, verifica se o banner de arranque declara o estado real.

**Allowlist escrita à mão.** Um detector que partilha o modo de falha do defeito que persegue —
esquecer uma entrada na lista produz o mesmo silêncio que esquecer a chamada. Deriva a lista do
registo único por onde tudo passa obrigatoriamente.

**Banner que mente.** Os ~50 avisos de postura do arranque são a documentação operacional do
nó. Se mudares o que um subsistema exige para compor e não actualizares a linha que o declara,
o operador passa a ler uma afirmação falsa — e vai acreditar nela.
