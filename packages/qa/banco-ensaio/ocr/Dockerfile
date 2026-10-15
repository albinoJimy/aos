# Imagem de medição do OCR do banco de ensaio (AOS-529): Tesseract 5 com os dados `por` e `eng`, e o
# `pdftoppm` (poppler) para o PDF. NÃO é imagem de produção: é o motor que o `aos-ensaio ocr --motor
# tesseract` corre, sem rede, com os limites do executor gVisor.
#
# Base fixada pelo digest (debian:12, bookworm); os pacotes vêm do repositório oficial do Debian.
# Construir precisa de rede (o `apt-get`) e é decisão do dono; correr não precisa:
#
#   docker build -t aos-ensaio-ocr:tesseract packages/qa/banco-ensaio/ocr
#   docker image inspect aos-ensaio-ocr:tesseract --format "{{.Id}}"
#
# A imagem construída no posto não tem digest de registo: o banco aceita-a pelo ID que o segundo
# comando imprime (`--imagem sha256:<64 hex>`). Os pacotes não têm versão fixada (o Debian só serve
# a última da bookworm), por isso a imagem identifica-se pelo ID e não por este ficheiro; o
# relatório leva a versão do Tesseract que ela traz.
FROM debian@sha256:f37a335e82bca302e955fa39f9dfe28f1be618f016f8a2b56318e5a5111afc26
RUN apt-get update \
 && apt-get install -y --no-install-recommends tesseract-ocr tesseract-ocr-por tesseract-ocr-eng poppler-utils \
 && rm -rf /var/lib/apt/lists/*
