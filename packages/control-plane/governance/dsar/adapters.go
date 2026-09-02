package dsar

import audit "github.com/aos-ref/platform/audit"

// auditStore adapta o [*audit.Shredder] (crypto-shredding do KeyVault por-titular,
// AOS-083) à porta [ShreddableKeyStore]. O mesmo Shredder pode servir também de
// [HoldOracle] do fluxo (implementa Held), centralizando a lógica de legal hold
// por-partição no audit sem a duplicar aqui.
type auditStore struct {
	name string
	sh   *audit.Shredder
}

// AuditStore liga o crypto-shredding do audit ao fluxo DSAR sob o rótulo dado.
func AuditStore(name string, sh *audit.Shredder) ShreddableKeyStore {
	return auditStore{name: name, sh: sh}
}

func (a auditStore) Name() string { return a.name }

// Shred delega no audit.Shredder: destrói a KEK do titular (idempotente) e é ele
// próprio fail-closed sob legal hold (devolve audit.ErrLegalHold), pelo que o store
// nunca destrói uma chave retida mesmo que o fluxo o invocasse.
func (a auditStore) Shred(subjectID string) error { return a.sh.Shred(subjectID) }

// redactionShredder é o subconjunto do redaction.InMemoryKeySource (ou de um KMS
// equivalente) que o adaptador precisa: destruir a chave de tokenização por-titular.
// A chave devolvida é DELIBERADAMENTE ignorada — o segredo nunca é exposto (ADR-006).
type redactionShredder interface {
	Shred(subject string) (key []byte, existed bool)
}

// redactionStore adapta um KeySource shreddable (tokenização de PII, AOS-091) à
// porta [ShreddableKeyStore].
type redactionStore struct {
	name  string
	ks    redactionShredder
	holds HoldOracle
}

// barreiraDeDestruicao é a porta OPCIONAL que um [HoldOracle] pode implementar para que um passo
// de destruição EXCLUA a colocação de um legal hold enquanto decorre. O `*audit.LegalHold`
// satisfá-la; um oráculo de teste pode não a ter.
type barreiraDeDestruicao interface {
	BeginDestruction() func()
}

// RedactionStore liga o crypto-shredding da redação ao fluxo DSAR sob o rótulo dado.
//
// O `holds` é OBRIGATÓRIO, e é o ponto: até 2026-08-22 este adaptador construía-se sem ele e
// destruía a chave de tokenização SEM consultar preservação nenhuma e SEM barreira. Não era
// alcançável — o nó compõe só o [AuditStore] —, mas estava exportado, e a invariante que o
// `POST /dsar/hold` publica é ABSOLUTA:
//
//	um 200 do /dsar/hold significa que NENHUMA destruição posterior deixa de ver este hold.
//
// Uma invariante com uma excepção é uma invariante que ninguém consegue citar. Compor este
// adaptador era o suficiente para a reintroduzir, e nada o impedia. Agora não se consegue
// construir sem cobertura de preservação.
func RedactionStore(name string, ks redactionShredder, holds HoldOracle) ShreddableKeyStore {
	return redactionStore{name: name, ks: ks, holds: holds}
}

func (r redactionStore) Name() string { return r.name }

// Shred destrói a chave de tokenização do titular. A chave devolvida é IGNORADA
// (nunca logada nem propagada); existed=false (chave ausente/já apagada) é um no-op
// idempotente sem erro — após o shred qualquer token do titular fica irresolúvel.
//
// FAIL-CLOSED sob legal hold, e a barreira envolve a leitura E a destruição — a MESMA forma de
// [audit.Shredder.Shred], deliberadamente.
//
// PORQUE AQUI E NÃO NO FLUXO: o fluxo já re-consulta o hold antes de cada store, mas fora de
// qualquer barreira — sobra a janela `Held()`→`Shred()` que o achado 1.9 nomeia. Tomá-la no ciclo
// do fluxo seria o desenho óbvio e ANINHARIA a barreira dentro da que o `audit.Shredder.Shred`
// já toma, e um `RLock` recursivo do mesmo `RWMutex` bloqueia para sempre assim que um escritor
// fique à espera. Por-store não aninha.
func (r redactionStore) Shred(subjectID string) error {
	if r.holds == nil {
		// Não deveria acontecer (o construtor exige-o), mas destruir sem saber é o pior desfecho
		// possível: recusa-se.
		return audit.ErrLegalHold
	}
	if b, ok := r.holds.(barreiraDeDestruicao); ok {
		defer b.BeginDestruction()()
	}
	if r.holds.Held(subjectID) {
		return audit.ErrLegalHold
	}
	_, _ = r.ks.Shred(subjectID)
	return nil
}

// ---------------------------------------------------------------------------
// STEP-LEDGER — a metade EM MEMÓRIA do apagamento (AOS-290)
// ---------------------------------------------------------------------------

// subjectForgetter é o subconjunto do `durable.StepLedger` de que este adaptador precisa:
// remover da projecção em memória as entradas de um titular. Interface local e mínima, no
// molde do [redactionShredder] — o pacote não importa o kernel para adaptar duas linhas.
type subjectForgetter interface {
	ForgetSubject(subject string) int
}

// ledgerStore adapta o step-ledger à porta [ShreddableKeyStore].
type ledgerStore struct {
	name string
	lg   subjectForgetter
}

// StepLedgerStore liga a poda em memória do step-ledger ao fluxo DSAR sob o rótulo dado.
//
// # PORQUE O LEDGER É UM STORE DE APAGAMENTO
//
// O `StepLedger` é composto UMA vez e partilhado por todos os runs do nó, e o seu mapa guarda
// o resultado de cada passo EM CLARO — o WAL leva o cifrado, a memória não. Media-se (AOS-290):
// destruída a KEK do titular, `OpenContent` sobre o blob do WAL falhava e `Applied` continuava
// a devolver o payload em claro. O apagamento do Art. 17 era real no disco e ficção em memória.
//
// # PORQUE ESTE STORE NÃO RE-CHECA O LEGAL HOLD, ao contrário do [RedactionStore]
//
// É uma diferença deliberada, e inverte-se aqui porque a operação é de OUTRA natureza. O
// RedactionStore DESTRÓI uma chave: é irreversível, e recusar sob hold é a postura segura. Este
// não destrói nada — larga uma CÓPIA EM MEMÓRIA de dados cuja chave o fluxo já destruiu (ou vai
// destruir na mesma passagem). Recusá-lo sob hold produziria o pior desfecho possível: a KEK
// destruída pelo store do audit e o texto claro retido aqui, que é EXACTAMENTE o defeito que
// este adaptador existe para fechar.
//
// A enforcement do hold continua a valer e não depende deste store: o [Flow] re-consulta o
// [HoldOracle] IMEDIATAMENTE ANTES de invocar `Shred` em cada store. Se o hold estiver posto, o
// fluxo não chega aqui — e não chega a nenhum dos outros.
func StepLedgerStore(name string, lg subjectForgetter) ShreddableKeyStore {
	return ledgerStore{name: name, lg: lg}
}

func (l ledgerStore) Name() string { return l.name }

// Shred remove as entradas do titular da projecção em memória do ledger. Idempotente e sem
// erro: um titular sem entradas é um no-op, como a porta exige. Nunca devolve nem regista o
// que removeu além da contagem — o payload não sai daqui para lado nenhum.
func (l ledgerStore) Shred(subjectID string) error {
	if l.lg == nil {
		// Store composto SEM ledger nenhum: não há o que apagar, e devolver erro faria o
		// fluxo reportar erasure parcial sobre uma superfície que não existe.
		//
		// Isto NÃO cobre o caso do ponteiro tipado nil (um `*StepLedger` nil dentro de uma
		// interface é uma interface não-nil e passa por aqui). Esse caso é do lado do
		// ledger, cujo `ForgetSubject` tem guarda de receptor nil. São duas guardas para
		// dois casos, não redundância.
		return nil
	}
	_ = l.lg.ForgetSubject(subjectID)
	return nil
}
