package dsar

// ---------------------------------------------------------------------------------------------
// A CADEIA NÃO AFIRMA O QUE NINGUÉM VERIFICOU.
//
// Achado 1.6 da varredura adversarial de 2026-08-21, demonstrado com um vault que aceita o DELETE
// e mantém a chave:
//
//	HTTP 500 ao requerente           ← o chamador NÃO recebe afirmação falsa
//	WORM: dsar.key_destroyed / allow ← BYTE-IDÊNTICO ao caso honesto
//	prontidão: 1 chave por confirmar
//
// O 500 já existia (o handler consulta a custódia DEPOIS). O que faltava era a cadeia: o registo
// tamper-evident — o que sobrevive a tudo o resto, e o único que um regulador vai ler anos depois
// — dizia «apagado» exactamente com os mesmos bytes nos dois casos.
//
// Um log append-only que regista o efeito ANTES de o confirmar é a mesma raiz transversal que já
// apareceu noutros pontos deste repositório. Aqui a ordem inverte-se: pergunta-se primeiro.
// ---------------------------------------------------------------------------------------------

// ShredConfirmer é a porta OPCIONAL de CONFIRMAÇÃO do crypto-shred.
//
// PORQUE É OPCIONAL, e não é indulgência: a porta [ShreddableKeyStore] não tem canal de erro no
// `Shred`, e nem toda a custódia sabe responder. Um vault em memória destrói e sabe-o; o Vault
// Transit relê a chave e exige 404; um KMS de terceiros pode não expor a pergunta. Custódias que
// não sabem responder NÃO ganham uma resposta inventada — não implementam a porta, e o fluxo
// mantém o comportamento anterior.
//
// FAIL-CLOSED quando implementada: um erro NÃO devolvido é uma confirmação. Devolver `nil` é
// afirmar «esta chave deixou de existir», e é sobre essa afirmação que a cadeia sela.
type ShredConfirmer interface {
	// ShredConfirmed devolve nil se a destruição da KEK deste titular está CONFIRMADA.
	ShredConfirmed(subjectID string) error
}

// WithShredConfirmer injecta a confirmação de custódia. Sem ela o fluxo comporta-se como antes de
// 2026-08-22 — sela `dsar.key_destroyed` a seguir ao shred, sem perguntar.
func WithShredConfirmer(c ShredConfirmer) Option {
	return func(f *Flow) {
		if c != nil {
			f.confirmer = c
		}
	}
}
