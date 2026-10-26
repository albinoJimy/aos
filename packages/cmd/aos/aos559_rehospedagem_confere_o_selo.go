package main

// aos559_rehospedagem_confere_o_selo.go — A RE-HOSPEDAGEM DE UM RUN EM CURSO RECONSTITUI O GOAL
// DO SELO (AOS-559).
//
// # O defeito, na forma em que ele existia
//
// O AOS-520 abriu a TERCEIRA via de re-hospedagem: um `POST /runs` com um `run_id` cujo estado
// durável não é o inicial, não é suspenso e não é terminal — um run que ficou em `running` quando o
// processo morreu — é ADMITIDO em vez de recusado, «uma retoma por outro nome» (caso «(2-ter-ter)»
// de [NodeService.submitObservando]). E o comentário que a declarava prometia que «confere-se o
// Goal com o que está SELADO para o run».
//
// A conferência cobria TRÊS CAMPOS, não o Goal: o alias do modelo, a marca de fixação e o digest do
// perfil da rota ([Node.goalDoRunExistente]). Todo o resto chegava DO CORPO de quem submete — e
// medido campo a campo são onze: `Objective`, `System`, `Scope`, `MaxTurns`, `AllowedTools`,
// `Inputs`, `CompletionRequires`, `OutputFromTool`, `OutputSourceBinding`, `ConteudoPorHandle` e o
// `Principal`/`Subject`.
//
// # O que isso comprava, seguido até quem CONSOME
//
// Dois dos onze alargam autoridade, e não é suposição:
//
//   - **o `Objective` entra no tail como CONFIÁVEL** — `SegmentAuthority(TailObjective, …)` devolve
//     `taint.Trusted` ([agentruntime.SegmentAuthority]). É o único texto de quem submete que entra
//     trusted. Num run que já tem saída de tools untrusted no contexto, uma re-submissão punha lá
//     uma instrução nova e confiável, a meio;
//   - **uma lista-branca nil é SEM RESTRIÇÃO** — [referencemonitor.RunAllowsTool] devolve `true`
//     para `nil`, e o campo do corpo é `tools` com `omitempty` ([submitRequest]). Um re-POST que
//     simplesmente OMITA `tools` apagava a lista-branca do run: um run que é o trabalho de um nó de
//     um plano arranca com a lista estreita que o plano lhe pinou, e passava a poder chamar tudo o
//     que o token do run autoriza. Nunca acima do token — é alargamento face ao PLANO, não bypass
//     da mediação.
//
// E em todos os casos o `run_id` continuava o mesmo, e com ele a reserva de quota, o nível de
// autonomia, a residência selada e o log do run.
//
// # A fonte de verdade existia, e estava a ser ignorada
//
// O registo de retoma ([integration.ResumeRecord]) tem 24 dos 26 campos do Goal (eram 23 de 25
// antes de o AOS-561 acrescentar as entradas de media aos dois) — só a
// `Credential` (deliberado: nunca é persistida) e o `ConteudoPorHandle` (que é um pedido de layout,
// não um campo do run) ficam de fora. É escrito no ARRANQUE de cada hospedagem
// ([NodeService.persistCrashResumeRecord]), e é o MESMO artefacto que as outras duas vias de
// re-hospedagem usam, pelo MESMO método: `rec.GoalWith(credencial)` — a retoma pela API
// ([NodeService.Resume]) e a varredura de crash-resume ([NodeService.ResumeInterruptedRuns]).
// É o precedente do AOS-551: o veredicto amarra-se ao artefacto SELADO, não ao que o chamador
// fornece.
//
// # AS TRÊS CLASSES, campo a campo
//
// O Goal com que o run é re-hospedado é `registo.GoalWith(corpo.Credential)` — SEMPRE. O que esta
// conferência decide é se o corpo é recusado por divergir dele. Cada campo está numa classe, e
// nenhum entra do corpo sem classe:
//
//  1. IGNORADO ([camposIgnorados]) — o corpo fornece-o e o valor dele é LEGITIMAMENTE diferente do
//     selado, pelo que não há comparação possível aqui:
//
//     - `Credential` — fresca: o registo não a tem, e é o único ponto em que ela volta a entrar.
//     - `Objective` — o do registo está REDIGIDO pela ingestão (AOS-091/AOS-208), que corre DEPOIS
//     desta conferência, e o do corpo é cru. Compará-los exigiria correr a ingestão sobre o
//     objectivo DO CORPO — uma segunda chamada, com o seu próprio registo de audit, além da que
//     corre de qualquer modo sobre o objectivo selado. Em vez da recusa, a divergência fica
//     ATRIBUÍDA no log do nó ([NodeService.submitObservando] di-lo, sem o texto de nenhum dos
//     dois): o run continua com o objectivo SELADO, e quem submeteu sabe que o seu foi ignorado.
//     - `Inputs` — os do registo trazem o conteúdo que [atravessarArestasDoPlano] RESOLVEU e as
//     referências que [selarEntradasDoPlano] cunhou, e os dois correm ANTES do registo
//     ([NodeService.hostRun]); os do corpo vêm como quem submete os manda. Pela mesma razão, não
//     são comparáveis.
//     - `RetryNotice` — decidido pelo NÓ a partir da prova que ESTE pedido trouxe
//     ([apiHandler.avisoDaTentativa]), nunca do corpo. O valor que semeou o tail é o selado.
//
//  2. SÓ DO SELO ([camposSoDoSelo]) — o corpo não os fornece em sítio nenhum (ver o literal
//     `agentruntime.Goal{…}` em [apiHandler.handleSubmit]), pelo que chegam no valor-zero e vêm
//     todos do selo. Mas não se copiam em SILÊNCIO: um valor não-zero que não seja o selado é
//     recusado, porque é um submissor em-processo a afirmar o que a porta não tem como dizer.
//     São `Model`, `Tools`, `Skills`, `MemoryContext`, `AssemblyVersion`, `CompletionMode`,
//     `ProjectionVersion`, `RouteProfileDigest`, `ModelPinnedBy` e `ParentTraceParent`.
//
//  3. RECUSADO SE DIVERGIR — todos os outros, e é a classe POR OMISSÃO: a comparação percorre os
//     campos do Goal por reflexão, pelo que um campo NOVO que alguém acrescente ao `Goal` e à porta
//     cai aqui sem ninguém se lembrar dele. Hoje são `RunID`, `Scope`, `System`, `MaxTurns`,
//     `AllowedTools`, `CompletionRequires`, `OutputFromTool`, `OutputSourceBinding`,
//     `ConteudoPorHandle`, `Principal` e `Subject`. Um campo NÃO EXPORTADO recusa sem ser lido: a
//     reflexão não o deixa ler e `Interface()` sobre ele entraria em PANIC no caminho de admissão
//     do `POST /runs` — a promessa é «nunca aberto», e um panic não é nem fechado nem aberto.
//
// Dois desses onze não se comparam campo-a-campo, e a razão é de forma:
//
//   - `ConteudoPorHandle` NÃO EXISTE no registo e nunca pode existir. Conferi-lo contra o campo do
//     selo — que é então estruturalmente `false` — só sabia RECUSAR: um run que pediu o layout por
//     handle e o obteve era recusado na re-submissão FIEL, e a única saída era omitir o campo. O
//     equivalente selado é o LAYOUT em que o run ficou, e é com ele que se confere
//     ([agentruntime.HasPlanInputsByHandle]) — a 1.8.0 só se alcança a pedido
//     ([layoutDoRunNovo]), pelo que a equivalência é exacta nos dois sentidos.
//   - `CompletionRequires` é o único campo conferido que é SLICE **e** tem `omitempty` no registo,
//     e é por isso o único com a assimetria: uma lista VAZIA serializa sem a chave e volta `nil`,
//     pelo que um corpo com `completion_requires: []` divergiria de si próprio. (O `OutputFromTool`
//     e o `OutputSourceBinding` também têm `omitempty`, mas são strings e o round-trip é
//     simétrico — a afirmação «o único com `omitempty`» que aqui estava era falsa, e foi um achado
//     da segunda passagem.) Normaliza-se — e SÓ quando as duas são vazias, porque um corpo sem
//     contrato contra um selo COM contrato é a divergência que mais interessa recusar. O
//     `AllowedTools` NÃO é normalizado, de propósito: ali `nil` é «sem restrição» e vazia é «nega
//     todas», e o registo preserva a diferença.
//
// # PORQUE RECUSAR, E NÃO ACEITAR-E-IGNORAR
//
// A sobreposição pelo selo já tornava a divergência INÓCUA — o run corre com o que está selado,
// venha no corpo o que vier. A recusa não é a defesa; é a ATRIBUIÇÃO. É o molde que esta mesma
// porta já aplica em três sítios — o `conteudo_por_handle`, o `inputs[].from_run` e o
// `requires_first_turn_trusted` —, e pela razão que lá está escrita: aceitar o campo e ignorá-lo
// daria a quem submete a ideia de que o run mudou, «que seria a afirmação ao contrário». Onde a
// comparação não é possível (classe 1), a atribuição é o LOG e não a recusa.
//
// # O PRINCIPAL CONFERE-SE PELA REGRA DA RETOMA, E NÃO CAMPO A CAMPO
//
// `Principal` está na classe 3, mas NÃO por `reflect.DeepEqual` sobre a struct: tem a sua própria
// função ([principalDivergeDoSelo]), e a razão é a diferença entre o que identifica o RUN e o que
// identifica QUEM CHAMA.
//
// O que identifica o run são os quatro eixos que o `POST /runs` resolve da credencial VERIFICADA à
// porta e que o registo selou: agente, humano, mandato e chave do mandato. Compará-los é comparar a
// credencial fresca com a cadeia selada, e é EXACTAMENTE a regra do passo (2-quater) do
// `resume.go` — cada um só quando o selo o tem, porque um selo escrito num nó que não verificava
// credencial tem-nos vazios, e esse run continua a ser retomável, como lá.
//
// O `NHIID` E O `REQUESTEDBY` NÃO SE COMPARAM, e isto foi um achado da SEGUNDA passagem da revisão:
// em modo soberano o `NHIID` do Goal é o principal OIDC de QUEM CHAMA o nó, sobreposto na porta
// ([apiHandler.handleSubmit] di-lo: «o campo de corpo auto-declarado `req.PrincipalNHI` aqui é
// IGNORADO»), e não o agente do run. Comparar a struct inteira amarrava a re-hospedagem ao MESMO
// chamador autenticado — e `AOS_PLAN_DRAINERS` é uma LISTA: o segundo drenador a pegar no run
// recebia 409 e não havia corpo nenhum que o resolvesse, porque o campo não vem do corpo. E não
// comprava integridade: o Goal que segue é o SELADO, pelo que o run continua sob a identidade com
// que foi selado, venha quem vier. Custava a recuperação, no caminho que existe para recuperar.
//
// CONSEQUÊNCIA DECLARADA, gémea da que o `resume.go` já declara: rodar o mandato ou a sua chave
// torna os runs em curso sob a anterior não re-hospedáveis por esta via. É o preço de a
// re-hospedagem exigir a mesma autoridade, e o operador escolhe o momento da rotação. (O
// `MandateAdmitsRequester` do AOS-439 continua a correr à porta, para todo o `POST /runs`, e não se
// repete aqui.)
//
// # SEM SELO NÃO SE RE-HOSPEDA, e as duas ausências não são a mesma
//
// O registo é escrito em FAIL-SOFT (uma falha não aborta o run), pelo que pode faltar. E há uma
// ausência mais larga, que não é do run: o nó pode não compor `ResumeRecords` nenhum — ele vive
// dentro do four-eyes gate, que só existe com `AOS_APPROVERS` configurado ([Bootstrap]), enquanto a
// máquina de estados que diz que o run está `running` é composta sempre. Nesse nó NENHUM run é
// reconstituível, e a via inteira recusa.
//
// As duas decidem-se pelo lado seguro, com causas SEPARADAS, porque o remédio do operador é
// diferente e porque uma delas é uma postura do nó e não um acidente de um run
// ([causaNoNaoSelaRegistoDeRetoma] e [causaSemRegistoDeRetoma]).
//
// E NÃO É «RECUPERÁVEL», ao contrário das recusas pelo perfil fixado: um run sem selo não é
// reconstituível por via NENHUMA — a retoma pela API devolve [ErrNoResumeRecord] e a varredura de
// crash-resume salta-o —, e não há reaper que o feche. O que esta via fazia era reconstituí-lo DO
// CORPO, que é a afirmação de que o corpo é o selo. A recusa não cria o run preso; expõe-o. O log
// do nó diz exactamente isto, em vez de oferecer um remédio que não se aplica.
//
// O run fica como estava — `running`, sem lease, sem quota e sem uma escrita.
//
// # O QUE FICA DE FORA, nomeado
//
//   - A JANELA ENTRE O SELO E O PRIMEIRO EVENTO continua aberta, e é o reaper por run (AOS-554).
//   - ESTA VIA NÃO LEVA PLANO DE REPLAY, pelo que os turnos já capturados voltam a interrogar o
//     modelo em vez de serem reproduzidos. É a outra metade da promessa «tem o contrato da retoma»,
//     tem ticket próprio — **AOS-560** — e não se fecha aqui: é sobre a REPRODUÇÃO dos turnos, não
//     sobre a origem dos campos do Goal.
//   - O `max_turns` do corpo é CLAMPADO à porta ([apiHandler.handleSubmit]) antes de chegar ao Goal,
//     e o registo selou o valor clampado. Se o tecto (`AOS_MAX_TURNS`) mudar entre as duas
//     hospedagens, uma re-submissão fiel diverge e é recusada. Fail-closed, recuperável: o run fica
//     `running` e repor o tecto volta a admiti-la.
//   - A conferência NÃO lê o snapshot `run.toolset.frozen`, que teria a oferta congelada do run. É
//     o mesmo limite que o AOS-557 declarou, e pela mesma razão: lê-se repondo-o no registo de tool
//     sets por-run, estado partilhado que o `Run` repõe e larga a seguir. O selo do registo cobre o
//     que esta via precisa — a lista-branca — e o snapshot não a tem
//     ([integration.ApplyFrozenToGoal] só estreita a OFERTA).

import (
	"reflect"

	modelgateway "github.com/aos-ref/platform/model-gateway"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
)

// Causas de recusa de [ErrRunExistenteRecusado] acrescentadas pelo AOS-559. Vocabulário fechado,
// como as do AOS-520 que vivem no `modelo_fixado.go`.
const (
	// causaNoNaoSelaRegistoDeRetoma — este nó não compõe `ResumeRecords`: não sela o Goal de run
	// nenhum e não reconstitui nenhum. É uma POSTURA do nó (o four-eyes gate não está composto,
	// por `AOS_APPROVERS` em falta), e não um acidente deste run — daí a causa própria.
	causaNoNaoSelaRegistoDeRetoma = "no_nao_sela_registo_de_retoma"
	// causaSemRegistoDeRetoma — o nó sela registos de retoma e este run não tem nenhum: não há selo
	// de que reconstituir o Goal, e o corpo não é o selo.
	causaSemRegistoDeRetoma = "sem_registo_de_retoma"
	// causaCorpoDivergeDoSelo — o corpo do `POST /runs` não é o que está selado para o run. O
	// campo que diverge vai no erro, para o log do operador; a resposta da rota é a uniforme.
	causaCorpoDivergeDoSelo = "corpo_diverge_do_selo"
)

// remedioDoRunExistenteRecusado devolve o que o operador pode FAZER sobre uma recusa desta via,
// pela causa dela.
//
// Existe porque a linha do log oferecia os três remédios da recusa pelo perfil fixado a TODAS as
// causas — e nenhum dos três se aplica às do AOS-559 (achado da revisão adversarial). Um remédio
// que não se aplica é pior do que remédio nenhum: manda o operador repor um perfil que está bem,
// e esconde que o run não tem saída.
func remedioDoRunExistenteRecusado(err error) string {
	switch causaDoRunExistenteRecusado(err) {
	case causaNoNaoSelaRegistoDeRetoma:
		return "este no NAO compoe registos de retoma (AOS_APPROVERS em falta, logo sem four-eyes gate): nao sela o Goal de run nenhum e NENHUM run e reconstituivel nele — nem por esta via, nem pela retoma da API, nem pela varredura de crash-resume. O run fica em `running` e nao ha saida neste no: continue-o num no que componha registos de retoma"
	case causaSemRegistoDeRetoma:
		return "este run NAO tem registo de retoma (a escrita e fail-soft e falhou, ou o run morreu antes dela): nao ha selo de que reconstituir o Goal, e o corpo nao e o selo. NAO e recuperavel — a retoma da API recusa-o pela mesma razao e a varredura de crash-resume salta-o. O run fica em `running`"
	case causaCorpoDivergeDoSelo:
		// NAO promete um corpo que resolva: em tres divergencias nao existe nenhum, e esta funcao
		// existe exactamente para nao dar remedios que nao se aplicam (achado da 2.a passagem).
		return "o campo que diverge vem a seguir. Se for um campo do pedido, re-submeta o corpo que esta SELADO para o run; se for `Principal`, `ConteudoPorHandle` ou `MaxTurns` NAO ha corpo que passe — a identidade vem da credencial VERIFICADA e nao de campos do pedido, o layout esta selado, e o tecto de turnos resolve-se repondo AOS_MAX_TURNS. Em qualquer dos casos o run continua em `running`, e a varredura de crash-resume retoma-o sem depender de quem chama"
	case causaFixacaoDiferenteDaGravada:
		return "o log do run contradiz-se sobre em que modelo esta fixado (o registo de retoma e o run.model_pinned nao dizem o mesmo): nao se escolhe um dos lados — investigue o stream do run"
	case causaMarcaSemFixacaoGravada:
		return "o Goal submetido afirma uma fixacao de modelo que o log do run nao tem; submeta-o sem a marca"
	case causaHistoriaDoRunIlegivel:
		return "o no nao conseguiu LER do seu log se o run existia (o stream do run, ou o dos registos de retoma): nao se decide sobre o que nao se leu. Duas classes, e a causa concreta vem a seguir: um Event Store que nao responde (reponha-o e re-submeta), ou um registo de retoma RECUSADO por nao ser o que o no escreveu — adulterado, em claro, de outro run, ou fora do Put — e ai o remedio nao e o substrato, e sim investigar quem escreveu no stream de governacao. O run continua em `running`"
	}
	// O vocabulário FECHADO do perfil fixado ([modelgateway.PinnedRouteProfileCauses]) tem o mesmo
	// remédio, e enumerá-lo caso a caso deixaria uma causa nova dele a cair no remédio genérico.
	for _, c := range modelgateway.PinnedRouteProfileCauses() {
		if causaDoRunExistenteRecusado(err) == c {
			return "reponha o perfil ou o registo em que o run esta fixado, ou retome-o num no que o sirva; o run continua em `running`"
		}
	}
	return "o run continua em `running`"
}

// arestasDeclaradas conta as arestas do plano que o corpo declarou (`inputs[].from_run` nao vazio).
//
// Serve so para o log: uma re-hospedagem descarta-as, porque a aresta foi atravessada na PRIMEIRA
// hospedagem e o resultado dela esta no selo. A contagem e a MESMA que
// [atravessarArestasDoPlano] usa para decidir se ha travessia a fazer.
func arestasDeclaradas(produtores []string) int {
	n := 0
	for _, r := range produtores {
		if r != "" {
			n++
		}
	}
	return n
}

// principalDivergeDoSelo diz se a identidade do corpo não é a que está selada para o run.
//
// A REGRA É A DO PASSO (2-quater) DO `resume.go`, e não uma comparação da struct: os quatro eixos
// que o `POST /runs` resolve da credencial VERIFICADA — agente, humano, mandato e chave do mandato
// — comparam-se CADA UM só quando o selo o tem. Um selo com o eixo vazio foi escrito por um nó que
// não verificava credencial, e esse run continua re-hospedável, exactamente como na retoma.
//
// O `NHIID` e o `RequestedBy` ficam DE FORA: em modo soberano identificam quem CHAMA o nó, não o
// agente do run, e amarrá-los ao selo tornava o run não re-hospedável pelo segundo drenador sem
// comprar integridade nenhuma — o Goal que segue é o selado. O porquê está no cabeçalho.
//
// Os restantes campos do `Principal` (`AgentClass`, `Board`, `Authority`, `DelegationChain`,
// `SubjectAuthority`) não são escritos pela porta em sítio nenhum: como a classe 2, o corpo tem de
// os trazer no valor-zero ou iguais aos selados.
func principalDivergeDoSelo(corpo, selado referencemonitor.Principal) bool {
	for _, eixo := range [][2]string{
		{corpo.AgentID, selado.AgentID},
		{corpo.UserID, selado.UserID},
		{corpo.MandateID, selado.MandateID},
		{corpo.MandateSigner, selado.MandateSigner},
	} {
		if eixo[1] != "" && eixo[0] != eixo[1] {
			return true
		}
	}
	corpo.NHIID, corpo.RequestedBy = selado.NHIID, selado.RequestedBy
	corpo.AgentID, corpo.UserID = selado.AgentID, selado.UserID
	corpo.MandateID, corpo.MandateSigner = selado.MandateID, selado.MandateSigner
	return !reflect.ValueOf(corpo).IsZero() && !reflect.DeepEqual(corpo, selado)
}

// camposIgnorados são os campos do [agentruntime.Goal] da CLASSE 1: o corpo fornece-os e o valor
// dele é legitimamente diferente do selado, pelo que não se comparam. A razão de cada um está no
// cabeçalho deste ficheiro, e é medida — não suposta.
var camposIgnorados = map[string]bool{
	"Credential": true,
	"Objective":  true,
	"Inputs":     true,
	// MediaInputs (AOS-561) — pela MESMA razão que `Inputs`, e medida: as do registo trazem as
	// referências que [selarMediaDoPedido] cunhou e JÁ NÃO TRAZEM OS BYTES (o selo limpa-os); as do
	// corpo vêm como quem submete as manda, com os bytes e sem referência. Não há comparação
	// possível aqui — e não é preciso que haja: o run é re-hospedado com o Goal SELADO, pelo que os
	// bytes do corpo são inertes (o selo salta uma entrada que já traz handle).
	"MediaInputs": true,
	"RetryNotice": true,
}

// camposSoDoSelo são os campos do [agentruntime.Goal] da CLASSE 2: o corpo do `POST /runs` não os
// escreve em sítio nenhum, pelo que chegam no valor-zero. Um valor não-zero que não seja o selado é
// RECUSADO — copiá-lo do selo em silêncio seria o aceitar-e-ignorar que este ficheiro condena, e o
// único chamador que os pode trazer é um submissor em-processo ([NodeService.SubmitObservando]).
var camposSoDoSelo = map[string]bool{
	"Model":              true,
	"Tools":              true,
	"Skills":             true,
	"MemoryContext":      true,
	"AssemblyVersion":    true,
	"CompletionMode":     true,
	"ProjectionVersion":  true,
	"RouteProfileDigest": true,
	"ModelPinnedBy":      true,
	"ParentTraceParent":  true,
}

// campoDoCorpoQueDiverge devolve o NOME do primeiro campo do Goal em que o corpo do `POST /runs`
// não é o que está selado para o run, ou vazio quando o corpo é fiel.
//
// A comparação é por REFLEXÃO sobre os campos do [agentruntime.Goal], e não uma lista escrita à
// mão, por uma razão de desenho: assim a classe «recusado se divergir» é a classe POR OMISSÃO, e um
// campo novo acrescentado ao Goal e à porta é conferido sem ninguém ter de se lembrar de o
// acrescentar aqui. O que se escreve são as duas outras classes, cada uma com a sua razão — e um
// campo esquecido numa delas falha fechado (recusa a mais), nunca aberto.
//
// `selado` é `registo.GoalWith(corpo.Credential)`: a credencial já lá está, pelo que não há nada a
// comparar nesse campo.
func campoDoCorpoQueDiverge(corpo, selado agentruntime.Goal) string {
	// O PEDIDO DO LAYOUT POR HANDLE confere-se contra o LAYOUT SELADO e não contra o campo do selo,
	// que é estruturalmente `false` — ver o cabeçalho. A 1.8.0 só se alcança a pedido, pelo que a
	// equivalência vale nos dois sentidos: pedi-la num run que não está nela é divergir, e não
	// pedi-la num run que está nela também.
	if corpo.ConteudoPorHandle != agentruntime.HasPlanInputsByHandle(selado.AssemblyVersion) {
		return "ConteudoPorHandle"
	}
	// O CONTRATO DE CONCLUSÃO é o único campo conferido com `omitempty` no registo: `[]` volta
	// `nil`. As duas formas dizem o mesmo — «sem contrato» —, e normalizam-se SÓ quando as duas
	// estão vazias. O `AllowedTools` não se normaliza: ali as duas formas dizem coisas opostas.
	if len(corpo.CompletionRequires) == 0 && len(selado.CompletionRequires) == 0 {
		corpo.CompletionRequires = selado.CompletionRequires
	}

	c, s := reflect.ValueOf(corpo), reflect.ValueOf(selado)
	t := c.Type()
	for i := 0; i < t.NumField(); i++ {
		campo := t.Field(i)
		nome := campo.Name
		// Um campo não exportado não se lê por reflexão: recusa, para a classe por omissão
		// continuar a ser «fechado» em vez de um panic.
		if !campo.IsExported() {
			return nome
		}
		switch {
		case nome == "ConteudoPorHandle" || camposIgnorados[nome]:
			// Já conferido acima, ou sem comparação possível.
		case nome == "Principal":
			if principalDivergeDoSelo(corpo.Principal, selado.Principal) {
				return nome
			}
		case camposSoDoSelo[nome]:
			// Ou o corpo não o diz, ou diz exactamente o que o selo diz.
			if !c.Field(i).IsZero() && !reflect.DeepEqual(c.Field(i).Interface(), s.Field(i).Interface()) {
				return nome
			}
		case !reflect.DeepEqual(c.Field(i).Interface(), s.Field(i).Interface()):
			return nome
		}
	}
	return ""
}
