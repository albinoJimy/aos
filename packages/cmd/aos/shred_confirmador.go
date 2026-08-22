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
	c, ok := v.(shredConfirmer)
	if !ok {
		return nil
	}
	return confirmadorDeShred{c: c}
}
