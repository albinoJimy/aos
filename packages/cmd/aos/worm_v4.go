package main

// worm_v4.go — O WORM v4 ESCREVE-SE SÓ QUANDO O OPERADOR O LIGA (AOS-439, expand/contract).
//
// O v4 acrescenta ao selo de cada decisão quem pediu o run (`requested_by`) e sob que mandato
// correu (`mandate_id`). Um binário anterior a esta release não conhece o v4 e recusa arrancar
// sobre um WORM que o contenha — o arranque re-verifica a cadeia fail-closed. Se o v4 fosse o
// default, o primeiro registo selado depois do deploy cortava o rollback para sempre.
//
// Por isso: esta release LÊ e VERIFICA v4 sempre, e ESCREVE v3 por omissão. `AOS_AUDIT_WRITE_V4=1`
// liga a escrita v4, e é o operador que o faz, depois de confirmar o deploy saudável. Com v3, o
// `requested_by` e o `mandate_id` continuam no evento de mediação do Event Store; só não entram no
// WORM.

import (
	"errors"
	"fmt"
	"strings"

	"github.com/aos-ref/platform/audit"
)

// ErrBadAuditWriteV4 — `AOS_AUDIT_WRITE_V4` com um valor que não é booleano. Aborta o arranque:
// ler um valor desconhecido como «desligado» esconderia o erro do operador.
var ErrBadAuditWriteV4 = errors.New("aos: AOS_AUDIT_WRITE_V4 invalida")

// parseAuditWriteV4 interpreta a variável, com o vocabulário de [parseDurableExecution].
func parseAuditWriteV4(s string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "0", "false", "f", "no", "n", "off":
		return false, nil
	case "1", "true", "t", "yes", "y", "on":
		return true, nil
	default:
		return false, fmt.Errorf("%w: valor %q", ErrBadAuditWriteV4, strings.TrimSpace(s))
	}
}

// versaoDeEscritaDoWORM é a versão que o WORM do nó escreve.
func versaoDeEscritaDoWORM(v4 bool) uint8 {
	if v4 {
		return audit.SchemaV4
	}
	return audit.SchemaV3
}

// wormV4PostureBanner declara, no arranque, que versão do WORM se escreve e o que isso custa.
// `composto` é false quando a Config trouxe o seu próprio store (testes, composition-root): aí a
// versão é a desse store, e o banner não afirma o que não controla.
func wormV4PostureBanner(v4, composto bool) []string {
	switch {
	case !composto:
		return []string{"WORM (AOS-439): store fornecido pela Config — a versao de escrita e a desse store; AOS_AUDIT_WRITE_V4 nao se aplica"}
	case v4:
		return []string{"WORM (AOS-439): escreve v4 — cada decisao sela requested_by e mandate_id. Um binario ANTERIOR a " +
			"esta release ja NAO arranca sobre este WORM (nao conhece o v4): o rollback para ele esta cortado"}
	default:
		return []string{"WORM (AOS-439): escreve v3 (por omissao) — le e verifica v4, mas nao o escreve, para o rollback " +
			"continuar possivel. requested_by e mandate_id ficam SO no evento de mediacao do Event Store, nao no WORM. " +
			"Depois de confirmar o deploy saudavel: AOS_AUDIT_WRITE_V4=1 (corta o rollback para binarios anteriores)"}
	}
}
