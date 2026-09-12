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

	// ErrControllerExigeClasse — um registo reidratado com actor [ControllerActor] cujo par
	// NÃO é uma chave de classe (prefixo [ClassPrefix]).
	//
	// A demoção automática governa a CLASSE, não a instância: os agent_id são cunhados por
	// run, logo uma demoção de instância seria efémera E, por sombrear a entrada de classe
	// que o PDP lê, poderia ELEVAR o efectivo (o defeito medido em AOS-090). Um registo do
	// controlador sobre uma chave de instância é, por isso, forja — e é RECUSADO na releitura
	// (saltado, nunca aplicado), não aceite por não ter para onde recuar.
	ErrControllerExigeClasse = errors.New("autonomy: registo do controlador fora de uma chave de classe (E_CONTROLLER_NOT_CLASS)")

	// ErrControllerDeveDescer — um registo reidratado com actor [ControllerActor] cujo
	// `New` NÃO é inferior ao nível reconstruído até esse ponto do stream.
	//
	// É o invariante que torna a demoção automática durável SEM assinatura. O controlador só
	// desce, por construção ([Controller.OnAnomaly] recusa qualquer alvo >= ao corrente).
	// Impor a mesma direcção na releitura fecha o vector que a ausência de prova abriria: o
	// [audit] EntryHash é um SHA-256 SEM chave, pelo que quem escreve o ficheiro consegue
	// apender um registo bem-formado com este actor — mas só consegue BAIXAR um nível, nunca
	// elevar. A comparação é contra o ESTADO RECONSTRUÍDO (não contra o `Old` do próprio
	// registo, que um forjador escolhe): um registo que se diga «L5->L3» sobre uma classe que
	// o replay tem em L1 é uma elevação disfarçada de demoção, e é recusado.
	//
	// PORQUE NÃO UMA ASSINATURA DO NÓ: assinar exigiria uma raiz de confiança fora do
	// ficheiro, e no modo de referência a chave do nó vive no MESMO disco do WORM — contra o
	// adversário que escreve o ficheiro, a assinatura não é fronteira nenhuma. O invariante
	// não depende de custódia de chave, depende só da direcção.
	ErrControllerDeveDescer = errors.New("autonomy: registo do controlador que nao desce face ao nivel reconstruido (E_CONTROLLER_NOT_DESCENDING)")
)
