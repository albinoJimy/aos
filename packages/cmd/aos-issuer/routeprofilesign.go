package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

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
// na linha de comandos é o CAMINHO. É uma seed em hex, como a dos outros subcomandos de assinatura,
// com os mesmos cuidados para os ficheiros que nascem em Windows ([loadRouteProfileKey]) — e com
// mensagens de erro próprias, que nunca repetem bytes da seed.
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
	// A assinatura não pode ir parar por cima do que se assinou, do relatório ou da chave.
	if *outPath != "" {
		for nome, outro := range map[string]string{"--entry": *entryPath, "--report": *reportPath, "--key-file": *keyFile} {
			if mesmoFicheiro(*outPath, outro) {
				return fmt.Errorf("route-profile-sign: --out e o mesmo ficheiro de %s — a assinatura nao se escreve por cima dele; nada foi escrito", nome)
			}
		}
	}
	// O DONO ASSINA O QUE VÊ: o resumo sai ANTES de a chave ser lida, com tudo o que o perfil
	// declara — os parâmetros do pedido e a versão da projecção mudam o que vai para o provider.
	resumoDoRegisto(diag, e)
	priv, err := loadRouteProfileKey(*keyFile)
	if err != nil {
		return err
	}
	signature := routeentry.Sign(entry, priv)
	// Conferência do que se acabou de fazer: a assinatura verifica contra a pública da chave lida.
	pub := priv.Public().(ed25519.PublicKey)
	if !routeentry.VerifySignature(entry, []byte(signature), pub) {
		return errors.New("route-profile-sign: a assinatura produzida nao verifica — nada foi escrito")
	}
	fmt.Fprintf(diag, "route-profile-sign: ASSINADO — alias %q, digest do registo %s\n", e.Profile.Requested, e.EntryDigest)
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
	priv, err := loadRouteProfileKey(*keyFile)
	if err != nil {
		return err
	}
	fmt.Fprintln(out, base64.StdEncoding.EncodeToString(priv.Public().(ed25519.PublicKey)))
	return nil
}

// resumoDoRegisto escreve o que vai ser assinado: tudo o que o perfil declara, o ambiente, a fatia
// e os três digests. Só identificadores, valores de vocabulário fechado e digests.
func resumoDoRegisto(w io.Writer, e routeentry.Entry) {
	p := e.Profile
	params := "nenhum"
	if p.Params != nil && !p.Params.IsZero() {
		m := p.Params.Declared()
		nomes := make([]string, 0, len(m))
		for k := range m {
			nomes = append(nomes, k)
		}
		sort.Strings(nomes)
		pares := make([]string, 0, len(nomes))
		for _, k := range nomes {
			pares = append(pares, k+"="+m[k])
		}
		params = strings.Join(pares, ", ")
	}
	projeccao := p.ProjectionVersion
	if projeccao == "" {
		projeccao = "nao declara (vale a do no)"
	}
	fmt.Fprintf(w, "route-profile-sign: VAI ASSINAR — alias %q, modelo esperado %q, wire %q, capacidades %v\n", p.Requested, p.ExpectedModel, p.WireClass, p.Capabilities)
	fmt.Fprintf(w, "route-profile-sign:   params do pedido: %s\n", params)
	fmt.Fprintf(w, "route-profile-sign:   projection_version: %s; devolver: nunca (por esta via so entram perfis que nao devolvem estado)\n", projeccao)
	fmt.Fprintf(w, "route-profile-sign:   board %q, regiao %q, fatia inicial %d%%\n", e.Board, e.Region, e.InitialSharePercent)
	fmt.Fprintf(w, "route-profile-sign:   digest do perfil %s; digest do registo %s; digest do relatorio %s\n", e.ProfileDigest, e.EntryDigest, e.ReportDigest)
}

// loadRouteProfileKey lê a seed (hex) da chave dos perfis de rota de um ficheiro. As mensagens são
// deste subcomando e NUNCA repetem bytes do ficheiro: o erro do descodificador de hex diz o byte
// que não percebeu, e esse byte é um byte da seed.
func loadRouteProfileKey(path string) (ed25519.PrivateKey, error) {
	raw, err := os.ReadFile(path) // #nosec G304 -- caminho dado pelo operador na linha de comandos
	if err != nil {
		return nil, errors.New("route-profile: nao foi possivel ler o ficheiro da chave dos perfis de rota (--key-file)")
	}
	texto, err := limparSeedHex(raw) // as seeds nascem em Windows: BOM e UTF-16 diagnosticam-se
	if err != nil {
		return nil, err
	}
	seed, err := hex.DecodeString(texto)
	if err != nil {
		return nil, errors.New("route-profile: a chave dos perfis de rota nao e uma seed em hexadecimal (o conteudo do ficheiro nao e repetido aqui)")
	}
	if len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("route-profile: a seed da chave dos perfis de rota tem de ter %d bytes (%d algarismos hexadecimais)", ed25519.SeedSize, 2*ed25519.SeedSize)
	}
	return ed25519.NewKeyFromSeed(seed), nil
}

// mesmoFicheiro diz se dois caminhos são o mesmo ficheiro: o mesmo caminho absoluto (sem
// distinguir a caixa, que no posto do dono é Windows) ou, existindo os dois, o mesmo ficheiro no
// disco (uma ligação, outro nome para o mesmo sítio).
func mesmoFicheiro(a, b string) bool {
	absA, errA := filepath.Abs(a)
	absB, errB := filepath.Abs(b)
	if errA == nil && errB == nil && strings.EqualFold(filepath.Clean(absA), filepath.Clean(absB)) {
		return true
	}
	infoA, errA := os.Stat(a)
	infoB, errB := os.Stat(b)
	return errA == nil && errB == nil && os.SameFile(infoA, infoB)
}
