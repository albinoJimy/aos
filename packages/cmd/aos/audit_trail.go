package main

// audit-trail — a VIA DE ACESSO à atribuição selada no WORM.
//
// Selar quem negou e porquê e não dar por onde ler seria repetir o padrão que este eixo
// existe para fechar: mecanismo sem via de acesso. Este subcomando é o gémeo do
// `wal-count` para o log de auditoria — READ-ONLY, sobre um WORM em disco, para correr
// num contentor EFÉMERO da mesma imagem com o nó principal PARADO (sem escritor
// concorrente).
//
// NÃO substitui uma superfície de administração autenticada: é diagnóstico de operador
// com acesso ao volume, exactamente como o wal-count. Por isso não vai à API nem exige
// credenciais — quem tem o ficheiro já tem o conteúdo.
//
// READ-ONLY DE VERDADE (AOS-373): abre por [audit.OpenFileStoreReadOnly] — NÃO anexa o
// ficheiro para append e NÃO trunca a cauda parcial. Ler a trilha deixou de ENCURTAR a
// prova: [audit.OpenFileStore] truncava uma cauda rasgada a validEnd antes de reabrir em
// append, pelo que correr esta ferramenta de LEITURA sobre a prova forense apagava o
// registo em voo — o próprio artefacto que uma investigação vai ver — e falhava a abrir num
// mount `:ro`. Agora o volume pode estar montado `:ro` e não é preciso ter o nó parado nem
// um escritor vivo: um abridor que não escreve não colide no audit_seq nem encolhe o log. O
// DANO INTERIOR continua a fechar a porta (fail-closed): um WORM corrompido recusa a
// reabertura mesmo em leitura, nunca se serve como íntegro.

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	audit "github.com/aos-ref/platform/audit"
)

// ErrAuditPathRequired — falta o --path (ficheiro WORM de auditoria).
var ErrAuditPathRequired = errors.New("aos: --path obrigatorio (ficheiro WORM de auditoria)")

// ErrAuditPartitionRequired — falta o --run (partição a listar; tipicamente o RunID).
var ErrAuditPartitionRequired = errors.New("aos: --run obrigatorio (particao do WORM; tipicamente o RunID)")

// cmdAuditTrail imprime a trilha de mediações seladas de uma partição, uma por linha:
// seq, veredicto, tool e capability e — quando NÃO é um allow — a ATRIBUIÇÃO (o gate que
// recusou, o código e a razão). É esta última parte que respondia antes só por bissecção.
//
// --denied-only reduz à pergunta que se faz numa auditoria: o que foi recusado, e porquê.
func cmdAuditTrail(args []string, w io.Writer) error {
	fs := flag.NewFlagSet("audit-trail", flag.ContinueOnError)
	fs.SetOutput(w)
	path := fs.String("path", "", "ficheiro WORM de auditoria")
	partition := fs.String("run", "", "particao a listar (tipicamente o RunID)")
	deniedOnly := fs.Bool("denied-only", false, "listar SO o que nao foi permitido")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*path) == "" {
		return ErrAuditPathRequired
	}
	if strings.TrimSpace(*partition) == "" {
		return ErrAuditPartitionRequired
	}

	store, err := audit.OpenFileStoreReadOnly(*path)
	if err != nil {
		return fmt.Errorf("aos: abrir WORM %q: %w", *path, err)
	}
	defer func() { _ = store.Close() }()

	recs, err := store.Read(context.Background(), *partition, 1, auditTrailMaxSeq)
	if err != nil {
		return fmt.Errorf("aos: ler particao %q do WORM: %w", *partition, err)
	}
	for _, r := range recs {
		if *deniedOnly && r.Decision == audit.DecisionAllow {
			continue
		}
		fmt.Fprintf(w, "seq=%d %s tool=%s cap=%s%s\n",
			r.AuditSeq, r.Decision, orDash(r.ToolID), orDash(r.Capability), atribuicao(r))
	}
	return nil
}

// auditTrailMaxSeq é o tecto do intervalo de leitura. O store devolve o que existir até
// ao head; um valor alto evita ter de o consultar primeiro.
const auditTrailMaxSeq = 1 << 40

// atribuicao formata a parte que responde "por quem e porquê". Vazia num allow, onde não
// há nada a atribuir — e vazia também num registo selado ANTES desta capacidade, que é a
// resposta honesta: aquele selo não a guardou.
func atribuicao(r audit.AuditRecord) string {
	if r.DeniedBy == "" && r.Code == "" && r.Reason == "" {
		return ""
	}
	out := ""
	if r.DeniedBy != "" {
		out += " denied_by=" + r.DeniedBy
	}
	if r.Code != "" {
		out += " code=" + r.Code
	}
	if r.Reason != "" {
		out += " reason=" + strconv0Quote(r.Reason)
	}
	return out
}

// orDash devolve "-" para um campo vazio, para as colunas não colapsarem.
func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// strconv0Quote cita a razão para uma razão multi-palavra não se confundir com o campo
// seguinte numa linha lida por olho ou por script.
func strconv0Quote(s string) string { return `"` + strings.ReplaceAll(s, `"`, `'`) + `"` }
