package main

// worm_v4.go — A ÉPOCA QUE O WORM ESCREVE SOBE SÓ QUANDO O OPERADOR A SOBE
// (AOS-439 e AOS-446 fase 1, expand/contract).
//
// O v4 acrescenta ao selo de cada decisão quem pediu o run (`requested_by`) e sob que mandato
// correu (`mandate_id`). O v5 acrescenta A CHAVE sob a qual esse mandato foi aceite
// (`mandate_signer`) — o que denuncia um `AOS_MANDATE_SIGNERS` trocado por quem tem root no host,
// que o `mandate_id` sozinho não denuncia (o id é escolhido por quem assina).
//
// Um binário anterior não conhece a época nova e recusa arrancar sobre um WORM que a contenha — o
// arranque re-verifica a hash-chain fail-closed. Se a época nova fosse o default, o primeiro
// registo selado depois do deploy cortava o rollback para sempre.
//
// Por isso: esta release LÊ e VERIFICA v4 e v5 sempre, e ESCREVE v3 por omissão.
// `AOS_AUDIT_WRITE_SCHEMA=4|5` sobe a época, e é o operador que o faz, depois de confirmar o
// deploy saudável. Com v3, o `requested_by`, o `mandate_id` e o `mandate_signer` continuam no
// evento de mediação do Event Store; só não entram no WORM.
//
// `AOS_AUDIT_WRITE_V4` CONTINUA A FUNCIONAR, e não é cortesia: já está no
// `docker-compose.prod.yml` e no `.env.example` do servidor, e a release que a introduziu corre
// hoje em produção. Tirá-la faria um deploy que a tivesse a `1` passar, em silêncio, a escrever
// v3 — uma DESCIDA de época sem ninguém pedir. Mantém-se como sinónimo de `=4`, e as duas em
// desacordo ABORTAM o arranque em vez de uma ganhar: quando a configuração se contradiz a si
// própria, nenhuma das leituras é a intenção do operador.

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/aos-ref/platform/audit"
)

// ErrBadAuditWriteV4 — `AOS_AUDIT_WRITE_V4` com um valor que não é booleano, ou
// `AOS_AUDIT_WRITE_SCHEMA` com uma época que esta release não escreve, ou as duas em desacordo.
// Aborta o arranque: ler um valor desconhecido como «desligado» esconderia o erro do operador.
var ErrBadAuditWriteV4 = errors.New("aos: AOS_AUDIT_WRITE_SCHEMA/AOS_AUDIT_WRITE_V4 invalida")

// parseAuditWriteV4 interpreta a variável booleana, com o vocabulário de [parseDurableExecution].
func parseAuditWriteV4(s string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "0", "false", "f", "no", "n", "off":
		return false, nil
	case "1", "true", "t", "yes", "y", "on":
		return true, nil
	default:
		return false, fmt.Errorf("%w: AOS_AUDIT_WRITE_V4 com valor %q", ErrBadAuditWriteV4, strings.TrimSpace(s))
	}
}

// parseAuditWriteSchema resolve a ÉPOCA de escrita a partir das duas variáveis. Devolve a versão
// que o store deve selar; vazias ⇒ [audit.CurrentSchemaVersion].
func parseAuditWriteSchema(schema, v4 string) (uint8, error) {
	ligadoV4, err := parseAuditWriteV4(v4)
	if err != nil {
		return 0, err
	}
	s := strings.TrimSpace(schema)
	if s == "" {
		if ligadoV4 {
			return audit.SchemaV4, nil
		}
		return audit.CurrentSchemaVersion, nil
	}
	n, cerr := strconv.Atoi(s)
	if cerr != nil || n < int(audit.SchemaV3) || n > int(audit.SchemaV5) {
		return 0, fmt.Errorf("%w: AOS_AUDIT_WRITE_SCHEMA=%q — esta release escreve %d, %d ou %d",
			ErrBadAuditWriteV4, s, audit.SchemaV3, audit.SchemaV4, audit.SchemaV5)
	}
	// AS DUAS EM DESACORDO ABORTAM. `AOS_AUDIT_WRITE_V4=1` com `AOS_AUDIT_WRITE_SCHEMA=5` podia
	// ler-se como «pelo menos v4, portanto 5» ou como «contradição»; e com `=3` só se pode ler
	// como contradição. Escolher por quem escreveu o `.env` é adivinhar a época que o WORM vai
	// gravar para sempre.
	if ligadoV4 && n != int(audit.SchemaV4) {
		return 0, fmt.Errorf("%w: AOS_AUDIT_WRITE_V4=1 (⇒ v%d) e AOS_AUDIT_WRITE_SCHEMA=%d dizem epocas diferentes — deixe so uma delas",
			ErrBadAuditWriteV4, audit.SchemaV4, n)
	}
	return uint8(n), nil
}

// versaoDeEscritaDoWORM é a versão que o WORM do nó escreve.
func versaoDeEscritaDoWORM(v uint8) uint8 {
	if v == 0 {
		return audit.CurrentSchemaVersion
	}
	return v
}

// wormV4PostureBanner declara, no arranque, que versão do WORM se escreve e o que isso custa.
// `composto` é false quando a Config trouxe o seu próprio store (testes, composition-root): aí a
// versão é a desse store, e o banner não afirma o que não controla.
func wormV4PostureBanner(versao uint8, composto bool) []string {
	if !composto {
		return []string{"WORM (AOS-439/AOS-446): store fornecido pela Config — a versao de escrita e a desse store; AOS_AUDIT_WRITE_SCHEMA nao se aplica"}
	}
	switch versaoDeEscritaDoWORM(versao) {
	case audit.SchemaV5:
		return []string{"WORM (AOS-446 fase 1): escreve v5 — cada decisao sela requested_by, mandate_id e mandate_signer " +
			"(a impressao digital da CHAVE que aceitou o mandato: um AOS_MANDATE_SIGNERS trocado deixa de poder " +
			"reproduzir o selo). Um binario ANTERIOR a esta release ja NAO arranca sobre este WORM: o rollback para " +
			"ele esta cortado"}
	case audit.SchemaV4:
		return []string{"WORM (AOS-439): escreve v4 — cada decisao sela requested_by e mandate_id, mas NAO o " +
			"mandate_signer (AOS-446 fase 1, epoca v5): a troca da chave pinada nao deixa rasto POR DECISAO; deixa-o " +
			"no registo das ancoras no arranque. Um binario ANTERIOR a esta release ja NAO arranca sobre este WORM: " +
			"o rollback para ele esta cortado. Para selar tambem a chave: AOS_AUDIT_WRITE_SCHEMA=5"}
	default:
		return []string{"WORM (AOS-439/AOS-446): escreve v3 (por omissao) — le e verifica v4 e v5, mas nao os escreve, " +
			"para o rollback continuar possivel. requested_by, mandate_id e mandate_signer ficam SO no evento de " +
			"mediacao do Event Store, nao no WORM. Depois de confirmar o deploy saudavel: AOS_AUDIT_WRITE_SCHEMA=5 " +
			"(corta o rollback para binarios anteriores)"}
	}
}
