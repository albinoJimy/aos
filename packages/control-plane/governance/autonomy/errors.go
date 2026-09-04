package autonomy

import "errors"

var (
	// ErrInvalidLevel — tentativa de fixar um nível fora do domínio L0–L5. Um nível
	// inválido é REJEITADO (nunca aplicado): a fronteira valida antes de mutar o
	// registo, evitando um estado que [Oversight] teria de tratar como fail-closed.
	ErrInvalidLevel = errors.New("autonomy: nivel invalido (fora de L0-L5)")
	// ErrEmptyPair — agente ou domínio vazios num [LevelRegistry.SetLevel]. O par
	// (agente, domínio) é a chave do registo; uma chave parcial é malformada.
	ErrEmptyPair = errors.New("autonomy: par (agente,dominio) incompleto")
	// ErrMissingReason — [LevelRegistry.SetLevel] sem motivo. AC5 exige que uma
	// alteração de nível seja um evento auditável COM motivo; um motivo vazio
	// selaria a alteração sem justificação e é REJEITADO antes de mutar/selar.
	ErrMissingReason = errors.New("autonomy: motivo obrigatorio na alteracao de nivel")
	// ErrMissingActor — [LevelRegistry.SetLevel] sem actor. AC5 exige atribuição de
	// responsabilidade; um actor vazio selaria a alteração de forma anónima
	// (quebrando o não-repúdio) e é REJEITADO antes de mutar/selar.
	ErrMissingActor = errors.New("autonomy: actor obrigatorio na alteracao de nivel")

	// ErrControladorNaoDesce — um registo reidratado cujo actor e [ControllerActor] pede um
	// nivel que NAO e inferior ao que o replay ja reconstruiu para o par.
	//
	// E o INVARIANTE que torna a democao automatica durável SEM assinatura. O controlador
	// so desce, por construcao ([Controller.OnAnomaly] recusa qualquer alvo >= ao actual,
	// controller.go). Impor essa mesma propriedade na releitura fecha o unico vector que a
	// ausencia de prova abriria: quem escreve no ficheiro do WORM consegue apender um
	// registo bem-formado com este actor — o EntryHash e um SHA-256 SEM chave — mas so
	// consegue BAIXAR um nivel, nunca elevar.
	//
	// PORQUE NAO UMA ASSINATURA. Assinar exigiria uma raiz de confianca fora do ficheiro, e
	// no modo de referencia a chave do no vive no MESMO disco do WORM: contra o adversario
	// que escreve o ficheiro, a assinatura nao e fronteira nenhuma. O invariante nao depende
	// de custodia de chave — depende so da direccao.
	//
	// A COMPARACAO E CONTRA O ESTADO DO REPLAY, nao contra os campos do proprio registo. Um
	// registo que se declare `L5 -> L3` num par que o replay tem em L1 seria uma ELEVACAO
	// disfarcada de democao; comparar `New` com o acumulado recusa-o.
	//
	// RESIDUAL DECLARADO: quem escreve o WORM ganha uma alavanca para PRENDER um par no
	// piso. E a troca deliberada — a alternativa e perder toda a democao legitima no
	// reinicio seguinte — e o adversario que a exerce ja tem piores opcoes.
	ErrControladorNaoDesce = errors.New("autonomy: registo de " + ControllerActor + " que nao desce face ao estado reidratado")
)
