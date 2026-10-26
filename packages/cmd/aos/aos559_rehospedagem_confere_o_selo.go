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
// O registo de retoma ([integration.ResumeRecord]) tem 23 dos 25 campos do Goal — só a
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
//     `ConteudoPorHandle`, `Principal` e `Subject`.
//
// Dois desses onze não se comparam campo-a-campo, e a razão é de forma:
//
//   - `ConteudoPorHandle` NÃO EXISTE no registo e nunca pode existir. Conferi-lo contra o campo do
//     selo — que é então estruturalmente `false` — só sabia RECUSAR: um run que pediu o layout por
//     handle e o obteve era recusado na re-submissão FIEL, e a única saída era omitir o campo. O
//     equivalente selado é o LAYOUT em que o run ficou, e é com ele que se confere
//     ([agentruntime.HasPlanInputsByHandle]) — a 1.8.0 só se alcança a pedido
//     ([layoutDoRunNovo]), pelo que a equivalência é exacta nos dois sentidos.
//   - `CompletionRequires` é o único campo conferido com `omitempty` no registo: uma lista VAZIA
//     serializa sem a chave e volta `nil`, pelo que um corpo com `completion_requires: []`
//     divergiria de si próprio. Normaliza-se — e SÓ quando as duas são vazias, porque um corpo sem
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
// # O PRINCIPAL CONFERE-SE DE GRAÇA, e isso não é acidente
//
// `Principal` está na classe 3, e é por aí que esta via passa a ter a conferência de identidade que
// a retoma pela API faz no seu passo (2-quater): o `POST /runs` escreve em
// `Principal.AgentID`/`UserID`/`MandateID`/`MandateSigner` o que resolveu da credencial VERIFICADA
// à porta, e o registo selou os do run. Compará-los é comparar a credencial fresca com a cadeia
// selada — agente, humano, mandato e chave do mandato, os quatro eixos que o `resume.go` enumera.
// Uma credencial que não verifique deixa-os vazios e também divergem: fail-closed.
//
// CONSEQUÊNCIA DECLARADA, gémea da que o `resume.go` já declara: rodar a chave do mandato torna os
// runs em curso sob a anterior não re-hospedáveis por esta via. É o preço de a re-hospedagem exigir
// a mesma autoridade, e o operador escolhe o momento da rotação. (O `MandateAdmitsRequester` do
// AOS-439 continua a correr à porta, para todo o `POST /runs`, e não se repete aqui.)
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
		return "re-submeta o corpo que esta SELADO para o run (o campo que diverge vem a seguir); o run continua em `running` e e re-hospedavel com o corpo certo"
	case causaFixacaoDiferenteDaGravada:
		return "o log do run contradiz-se sobre em que modelo esta fixado (o registo de retoma e o run.model_pinned nao dizem o mesmo): nao se escolhe um dos lados — investigue o stream do run"
	case causaMarcaSemFixacaoGravada:
		return "o Goal submetido afirma uma fixacao de modelo que o log do run nao tem; submeta-o sem a marca"
	case causaHistoriaDoRunIlegivel:
		return "o no nao conseguiu LER do seu log se o run existia (o stream do run, ou o dos registos de retoma): nao se decide sobre o que nao se leu — reponha o Event Store e re-submeta; o run continua em `running`"
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

// camposIgnorados são os campos do [agentruntime.Goal] da CLASSE 1: o corpo fornece-os e o valor
// dele é legitimamente diferente do selado, pelo que não se comparam. A razão de cada um está no
// cabeçalho deste ficheiro, e é medida — não suposta.
var camposIgnorados = map[string]bool{
	"Credential":  true,
	"Objective":   true,
	"Inputs":      true,
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
		nome := t.Field(i).Name
		igual := reflect.DeepEqual(c.Field(i).Interface(), s.Field(i).Interface())
		switch {
		case nome == "ConteudoPorHandle" || camposIgnorados[nome]:
			// Já conferido acima, ou sem comparação possível.
		case camposSoDoSelo[nome]:
			// Ou o corpo não o diz, ou diz exactamente o que o selo diz.
			if !c.Field(i).IsZero() && !igual {
				return nome
			}
		case !igual:
			return nome
		}
	}
	return ""
}
