package main

import dsar "github.com/aos-ref/control-plane/governance/dsar"

// confirmadorDeShred adapta a porta INTERNA [shredConfirmer] — que a custódia do nó já
// implementava — à porta [dsar.ShredConfirmer] que o fluxo consulta ANTES de selar.
//
// É um adaptador e não uma segunda implementação: a pergunta é a mesma e a resposta vem do mesmo
// sítio ([vaultKeyVault.shredConfirmed], que relê a chave Transit e exige 404). O que muda é QUEM
// pergunta e QUANDO — o handler perguntava DEPOIS de a cadeia já ter afirmado o apagamento.
type confirmadorDeShred struct{ c shredConfirmer }

// ShredConfirmed implementa [dsar.ShredConfirmer].
func (a confirmadorDeShred) ShredConfirmed(subjectID string) error {
	return a.c.shredConfirmed(subjectID)
}

// confirmadorDeShredDe devolve o adaptador se a custódia souber responder, e nil se não souber.
//
// Custódias que não sabem responder NÃO ganham uma resposta inventada: o `InMemoryKeyVault` de
// referência destrói e não implementa a porta, e o fluxo mantém o comportamento anterior. Ver a
// nota sobre opcionalidade em [dsar.ShredConfirmer].
func confirmadorDeShredDe(v any) dsar.ShredConfirmer {
	// A PORTA EXPORTADA PRIMEIRO, e a razão apareceu na revisão adversarial do AOS-328: o
	// `shredConfirmer` tem método NÃO-EXPORTADO, pelo que só um tipo declarado neste `package
	// main` o pode satisfazer. Uma custódia de terceiros noutro módulo — literalmente o cenário
	// que o `DEF-813` descreve — nunca conseguia implementá-lo, e o remédio que o banner
	// prescreve («implemente a porta na custódia») era impossível de seguir.
	//
	// `dsar.ShredConfirmer` é exportado e é a porta que o fluxo consulta de facto. Aceitá-la
	// directamente é o que torna a prescrição verdadeira.
	if c, ok := v.(dsar.ShredConfirmer); ok {
		return c
	}
	// A porta INTERNA continua a servir a custódia in-package (`*vaultKeyVault`), que a
	// implementa desde AOS-322 e cujo nome de método não vale a pena mudar.
	c, ok := v.(shredConfirmer)
	if !ok {
		return nil
	}
	return confirmadorDeShred{c: c}
}
