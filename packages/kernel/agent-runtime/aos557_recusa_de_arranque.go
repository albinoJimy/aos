package agentruntime

// aos557_recusa_de_arranque.go — A RECUSA DE ARRANQUE, NUMA SÓ IMPLEMENTAÇÃO (AOS-557).
//
// # O defeito que isto fecha
//
// O [Runtime.Run] tem portões fail-closed que recusam o run ANTES do primeiro turno e sem um
// único efeito. Dois deles são accionáveis por quem submete — o contrato de conclusão
// ([contratoPossivel]) e a origem declarada da saída ([origemPossivel]) —, e correm DEPOIS de o
// nó selar as entradas do plano em handles ([ADR-043] fase R2). Um run com um contrato que o nó
// não pode cumprir deixava, antes de fechar sem dar um turno, handles cunhados no stream e bytes
// cifrados em disco que ninguém apaga.
//
// Fechá-lo é correr a conferência MAIS CEDO, na hospedagem, antes do selo. E correr a mesma
// conferência em dois sítios é exactamente onde nasce o defeito seguinte: dois guardas com a
// mesma aparência e regras diferentes é pior do que um.
//
// # Porque é que isto é uma função, e não uma cópia
//
// [recusaDeArranque] é a implementação ÚNICA dos dois portões. O [Runtime.Run] chama-a com o
// `Completion` que já resolveu; quem hospeda o run chama [RefuseAtStartup], que resolve o
// `Completion` do mesmo Goal e delega na mesma função. Não há uma segunda regra que possa
// divergir — nem a comparação de nomes, nem a ordem das causas, nem os erros devolvidos.
//
// # O que difere entre as duas chamadas, e é a única coisa que difere
//
// A OFERTA. O portão do `Run` decide contra o [Goal.Tools] DEFINITIVO — o tool set congelado do
// run, cortado pela lista-branca. Quem hospeda o run ainda não o tem (o congelamento é o
// primeiro passo do `Run`), e por isso passa a oferta que conhece, por nome.
//
// Donde o contrato desta função pública: ela é a conferência que EVITA o selo, e não a garantia.
// A garantia continua a ser a do `Run`, que decide contra o conjunto congelado. Quem chama tem
// de passar uma oferta que seja um SUBCONJUNTO da que o `Run` vai ver — ou, em bom português,
// nunca pode refusar aqui o que o `Run` admitiria. Porque é que a oferta do nó cumpre isso está
// escrito no chamador (packages/cmd/aos/aos557_portoes_antes_do_selo.go), que é quem tem a
// medição.

// RefuseAtStartup devolve a recusa que o [Runtime.Run] faz ANTES do primeiro turno por um
// defeito de quem COMPÔS o run: o modo do veredicto fora do vocabulário, o contrato de conclusão
// impossível ([ErrImpossibleCompletionContract]) e a origem declarada da saída impossível ou mal
// formada ([ErrImpossibleOutputSource], [ErrBadOutputSourceBinding]). nil quando nenhuma se
// aplica.
//
// `offered` é a oferta contra a qual a pertença se decide, POR NOME — o que o chamador sabe que
// este run vai oferecer ao modelo. Substitui o [Goal.Tools] do `goal` recebido, que num chamador
// anterior ao congelamento do tool set está vazio e recusaria tudo. A lista-branca do run
// ([Goal.AllowedTools]) continua a ser a do Goal, e é conferida à parte pela MESMA regra do
// Reference Monitor.
//
// O VEREDICTO NÃO DEPENDE DE A OFERTA VIR CORTADA PELA LISTA-BRANCA; A CAUSA DEPENDE. Com a oferta
// inteira, uma tool da lista de fora cai no ramo «está fora da lista-branca»; com a oferta cortada,
// no ramo «não está no tool set». O erro-sentinela é o mesmo e o desfecho do run é o mesmo, mas o
// texto que quem submete lê não é — e o `Goal.Tools` que o [Runtime.Run] vê no nó JÁ vem cortado
// (quem o corta é o composition-root, ao aplicar o tool set congelado). Donde: quem usa esta função
// para antecipar a recusa do `Run` passa a oferta CORTADA, como o nó faz, e um teste amarra as duas
// causas contra o caminho real.
//
// SÓ O NOME IMPORTA, e isso não é uma suposição: os dois portões lêem [ToolSpec.Name] e mais nada
// do spec. [TestAOS557_RunERefuseAtStartupDaoAMesmaRecusa] amarra-o comparando as MENSAGENS das
// duas recusas num caso em que o `Run` vê specs com versão e digest e esta função vê specs só com
// nome: um portão que passasse a ler a versão ou o digest daria textos diferentes e ficaria
// vermelho, em vez de decidir com campos que esta função não tem para dar.
//
// NÃO TEM EFEITO NENHUM: não lê estado do runtime, não abre janela, não escreve evento nem span.
// É por isso que se pode fazer correr antes de qualquer selagem.
func RefuseAtStartup(goal Goal, offered []string) error {
	if err := goalValidavel(goal); err != nil {
		return err
	}
	goal.Tools = make([]ToolSpec, len(offered))
	for i, nome := range offered {
		goal.Tools[i] = ToolSpec{Name: nome}
	}
	conclusao, err := completionDoGoal(goal)
	if err != nil {
		return err
	}
	return recusaDeArranque(conclusao, goal)
}

// recusaDeArranque é a implementação ÚNICA dos dois portões accionáveis por quem submete, na
// ordem das causas: primeiro o contrato de conclusão (AOS-493), depois a origem da saída
// (AOS-497). `c` nil (veredicto desligado) ⇒ nenhum dos dois lê nada, como em todo o resto.
//
// A ORDEM FAZ PARTE DO CONTRATO. Um run com as duas declarações más é recusado pelo contrato, e
// é a mensagem do contrato que chega a quem submete pelo `GET /runs/{id}`. Trocá-la aqui mudava
// a causa que ele lê, sem mudar o desfecho.
func recusaDeArranque(c *Completion, goal Goal) error {
	if err := contratoPossivel(c, goal); err != nil {
		return err
	}
	return origemPossivel(c, goal)
}

// goalValidavel são as pré-condições do [Runtime.Run] que dependem SÓ do Goal: o id do run e o
// principal. É a implementação ÚNICA — o `rt.validate` chama-a depois de conferir a composição do
// runtime, e a [RefuseAtStartup] chama-a primeiro.
//
// # Porque é que ela subiu (AOS-557)
//
// São função apenas do Goal, logo o critério do ticket manda-as correr antes de qualquer selagem.
// E deixá-las em baixo DEIXAVA RESÍDUO, numa combinação que está medida: um Goal com `Subject`
// preenchido e `Principal.NHIID` vazio tem titular NÃO vazio ([Goal.Titular] cai no `Subject`),
// pelo que a quarentena do espaço de handles o aceita e o selo CUNHA — e só depois o `Run` recusa
// com [ErrNoPrincipal]. A porta HTTP do nó não chega a essa combinação (ali o principal verificado
// preenche os dois campos, ou nenhum), mas um `Submit` programático chega: o `Submit` só confere o
// id do run.
//
// A ORDEM NÃO MUDOU para quem chama o `Run`: as recusas de composição continuam a vir primeiro, e
// entre as duas do Goal a ordem é a de sempre.
func goalValidavel(goal Goal) error {
	switch {
	case goal.RunID == "":
		return ErrEmptyRunID
	case goal.Principal.NHIID == "":
		return ErrNoPrincipal
	}
	return nil
}
