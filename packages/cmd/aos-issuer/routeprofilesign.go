package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/aos-ref/platform/model-gateway/policy/routeentry"
)

// ---------------------------------------------------------------------------------------------
// A ASSINATURA DE UM REGISTO DE ENTRADA DE PERFIL DE ROTA (AOS-519).
//
// O nó carrega perfis de rota de uma pasta, por registos de entrada assinados pelo dono, e
// confere cada um no arranque. Quem assina é o dono, no seu posto, com uma chave SÓ para perfis
// de rota — não a do emissor, não a da allowlist, não a de quem aprova planos.
//
// A CHAVE VEM DE UM FICHEIRO. Nunca de uma variável de ambiente nem de um argumento: o que vai
// na linha de comandos é o CAMINHO. É a mesma seed em hex dos outros subcomandos de assinatura
// ([loadApproverKey]), com os mesmos cuidados para os ficheiros que nascem em Windows.
//
// NÃO SE ASSINA O QUE O NÓ RECUSARIA. Antes de assinar, o subcomando faz ao registo e ao
// relatório as MESMAS conferências que o nó faz no arranque, com o mesmo código: a leitura
// fechada do registo, o digest do perfil, a classe de estado, a imagem do proxy declarada, e o
// relatório citado — que confere o seu digest, é o que o registo cita, é do modo real, diz
// `qualificado`, e é desse perfil e dessa imagem. Com um veredicto `recusado` ou `inconclusivo`,
// ou com um relatório de um ensaio, não sai assinatura nenhuma. Ficam por conferir só as três
// coisas que dependem do nó: o board e a região dele, e a allowlist em vigor lá.
//
// O QUE A ASSINATURA DIZ. Que o dono da chave viu AQUELE registo, que cita AQUELE relatório. Não
// diz que o banco de ensaio correu: o relatório é produzido no posto de quem corre.
// ---------------------------------------------------------------------------------------------

// runRouteProfileSign assina um registo de entrada. `out` recebe SÓ a assinatura (base64) quando
// não há --out; o resumo do que foi assinado vai para `diag`.
func runRouteProfileSign(args []string, out, diag io.Writer) error {
	fs := flag.NewFlagSet("route-profile-sign", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	entryPath := fs.String("entry", "", "o registo de entrada a assinar (<rotulo>.registo.json)")
	reportPath := fs.String("report", "", "o relatorio de qualificacao que o registo cita (<rotulo>.relatorio.json)")
	keyFile := fs.String("key-file", "", "ficheiro com a seed (hex) da chave dos perfis de rota")
	outPath := fs.String("out", "", "onde escrever a assinatura (<rotulo>.registo.sig); vazio = stdout")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("route-profile-sign: %w", err)
	}
	if fs.NArg() != 0 || *entryPath == "" || *reportPath == "" || *keyFile == "" {
		return errors.New("route-profile-sign: uso: --entry <registo.json> --report <relatorio.json> --key-file <ficheiro> [--out <registo.sig>]")
	}
	entry, err := os.ReadFile(*entryPath) // #nosec G304 -- caminho dado pelo operador na linha de comandos
	if err != nil {
		return errors.New("route-profile-sign: nao foi possivel ler o registo de --entry")
	}
	report, err := os.ReadFile(*reportPath) // #nosec G304 -- caminho dado pelo operador na linha de comandos
	if err != nil {
		return errors.New("route-profile-sign: nao foi possivel ler o relatorio de --report")
	}
	// As conferências do nó, ANTES de a chave ser sequer lida.
	e, err := routeentry.ParseEntry(entry)
	if err != nil {
		return fmt.Errorf("route-profile-sign: o registo nao e assinavel: %w", err)
	}
	if e.ProxyImageDigest != routeentry.ProductionProxyImageDigest() {
		return fmt.Errorf("route-profile-sign: o registo nao e assinavel: causa=%s (a imagem do proxy que cita nao e a declarada para producao nesta versao)", routeentry.CauseProxyImageNotDeclared)
	}
	if err := routeentry.CheckReport(e, report); err != nil {
		return fmt.Errorf("route-profile-sign: o registo nao e assinavel: %w", err)
	}
	priv, err := loadApproverKey(*keyFile)
	if err != nil {
		return fmt.Errorf("route-profile-sign: chave dos perfis de rota: %w", err)
	}
	signature := routeentry.Sign(entry, priv)
	// Conferência do que se acabou de fazer: a assinatura verifica contra a pública da chave lida.
	pub := priv.Public().(ed25519.PublicKey)
	if !routeentry.VerifySignature(entry, []byte(signature), pub) {
		return errors.New("route-profile-sign: a assinatura produzida nao verifica — nada foi escrito")
	}
	fmt.Fprintf(diag, "route-profile-sign: ASSINADO — alias %q, modelo esperado %q, board %q, regiao %q, fatia inicial %d%%\n",
		e.Profile.Requested, e.Profile.ExpectedModel, e.Board, e.Region, e.InitialSharePercent)
	fmt.Fprintf(diag, "route-profile-sign: digest do perfil %s; digest do registo %s; digest do relatorio %s\n", e.ProfileDigest, e.EntryDigest, e.ReportDigest)
	fmt.Fprintf(diag, "route-profile-sign: ancora de confianca (AOS_MODEL_ROUTE_PROFILES_TRUST_ANCHOR) desta chave: %s\n", base64.StdEncoding.EncodeToString(pub))
	fmt.Fprintln(diag, "route-profile-sign: por conferir, porque so o no o sabe: o board e a regiao dele, e que a allowlist em vigor la autoriza o alias. A assinatura nao prova que o banco de ensaio correu")
	if *outPath == "" {
		fmt.Fprintln(out, signature)
		return nil
	}
	if err := os.WriteFile(*outPath, []byte(signature+"\n"), 0o600); err != nil {
		return errors.New("route-profile-sign: nao foi possivel escrever a assinatura em --out")
	}
	return nil
}

// runRouteProfileAnchor imprime a âncora de confiança — a chave PÚBLICA em base64 — da chave dos
// perfis de rota: o valor de AOS_MODEL_ROUTE_PROFILES_TRUST_ANCHOR do nó. Só material público.
func runRouteProfileAnchor(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("route-profile-anchor", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	keyFile := fs.String("key-file", "", "ficheiro com a seed (hex) da chave dos perfis de rota")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("route-profile-anchor: %w", err)
	}
	if fs.NArg() != 0 || *keyFile == "" {
		return errors.New("route-profile-anchor: uso: --key-file <ficheiro>")
	}
	priv, err := loadApproverKey(*keyFile)
	if err != nil {
		return fmt.Errorf("route-profile-anchor: chave dos perfis de rota: %w", err)
	}
	fmt.Fprintln(out, base64.StdEncoding.EncodeToString(priv.Public().(ed25519.PublicKey)))
	return nil
}
