package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/aos-ref/substrate/eventstore/natsjs"
)

// cmdNATSNKey é o gerador da credencial do nó para o cluster NATS (AOS-470), no molde de
// `wg genkey` / `wg pubkey`:
//
//	aos nats-nkey gerar                  imprime uma SEED nova (SU…) — redirige-a para um ficheiro 0400
//	aos nats-nkey publica [--key F]      lê a seed (de F, ou do stdin) e imprime a chave PÚBLICA (U…)
//
// A pública é o que vai para a `authorization` do servidor (linha `cliente` do cluster.conf de
// deploy/nats); a seed fica no host do nó e chega ao contentor por ficheiro montado
// (AOS_EVENTSTORE_NATS_NKEY_FILE). Vive no binário `aos` para não exigir o `nk` da NATS no host
// de quem opera: a imagem do nó já lá está, e `docker run --rm -i <imagem> nats-nkey …` basta.
//
// O stdin no `publica` existe porque a imagem corre como uid 65532: um ficheiro 0400 do root
// montado no contentor seria ilegível, e passar a seed por pipe evita abrir-lhe as permissões.
func cmdNATSNKey(args []string, in io.Reader, w io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("aos: nats-nkey exige um subcomando: gerar | publica [--key FICHEIRO]")
	}
	switch args[0] {
	case "gerar":
		if len(args) > 1 {
			return fmt.Errorf("aos: nats-nkey gerar não leva argumentos (a seed sai no stdout: redirija-a para um ficheiro com umask 077)")
		}
		seed, _, err := natsjs.GerarNKeyUtilizador(nil)
		if err != nil {
			return err
		}
		fmt.Fprintln(w, seed)
		return nil
	case "publica":
		fs := flag.NewFlagSet("nats-nkey publica", flag.ContinueOnError)
		fs.SetOutput(w)
		keyPath := fs.String("key", "", "ficheiro da seed nkey (SU…); vazio lê do stdin")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		var (
			k   *natsjs.NKey
			err error
		)
		if p := strings.TrimSpace(*keyPath); p != "" {
			k, err = natsjs.LerNKeyFicheiro(p)
		} else {
			var raw []byte
			raw, err = io.ReadAll(io.LimitReader(in, 4096))
			if err == nil {
				k, err = natsjs.ParseNKeySeed(semBOM(raw))
			}
		}
		if err != nil {
			return fmt.Errorf("aos: nats-nkey publica: %w", err)
		}
		fmt.Fprintln(w, k.Publica())
		return nil
	default:
		return fmt.Errorf("aos: nats-nkey: subcomando desconhecido %q (gerar | publica)", args[0])
	}
}

// stdinDoProcesso é o stdin que o despacho entrega ao `nats-nkey publica`. Variável para os
// testes do despacho não lerem o stdin real do `go test`.
var stdinDoProcesso io.Reader = os.Stdin
