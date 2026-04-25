### -------------------------------------------------------
### Stage 1: Build the Go daemon binary
### -------------------------------------------------------
FROM golang:1.25 AS builder

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /daemon ./cmd/daemon

### -------------------------------------------------------
### Stage 2: ZATCA SDK + Daemon runtime
### -------------------------------------------------------
FROM eclipse-temurin:21-jre-alpine

RUN apk add --no-cache jq bash

COPY zatca-einvoicing-sdk-238-R4.0.0 /SDK/zatca-einvoicing-sdk-238-R4.0.0

WORKDIR /SDK/zatca-einvoicing-sdk-238-R4.0.0

ENV FATOORA_HOME=/SDK/zatca-einvoicing-sdk-238-R4.0.0/Apps
ENV SDK_CONFIG=/SDK/zatca-einvoicing-sdk-238-R4.0.0/Configuration/config.json
ENV PATH="${FATOORA_HOME}:${PATH}"

# Fix config.json to use absolute paths inside the container
RUN cd Configuration && \
    parentDir="/SDK/zatca-einvoicing-sdk-238-R4.0.0" && \
    xsdPathFileName=$(jq -r '.xsdPath' defaults.json | xargs basename) && \
    enSchematronFileName=$(jq -r '.enSchematron' defaults.json | xargs basename) && \
    zatcaSchematronFileName=$(jq -r '.zatcaSchematron' defaults.json | xargs basename) && \
    certPathFileName=$(jq -r '.certPath' defaults.json | xargs basename) && \
    pkPathFileName=$(jq -r '.privateKeyPath' defaults.json | xargs basename) && \
    pihPathFileName=$(jq -r '.pihPath' defaults.json | xargs basename) && \
    usagePathFileName=$(jq -r '.usagePathFile' defaults.json | xargs basename) && \
    jq -n \
      --arg one "${parentDir}/Data/Schemas/xsds/UBL2.1/xsd/maindoc/$xsdPathFileName" \
      --arg two "${parentDir}/Data/Rules/schematrons/$enSchematronFileName" \
      --arg thr "${parentDir}/Data/Rules/schematrons/$zatcaSchematronFileName" \
      --arg fou "${parentDir}/Data/Certificates/$certPathFileName" \
      --arg fiv "${parentDir}/Data/Certificates/$pkPathFileName" \
      --arg six "${parentDir}/Data/PIH/$pihPathFileName" \
      --arg sev "${parentDir}/Data/Input" \
      --arg eight "${parentDir}/Configuration/$usagePathFileName" \
      '{"xsdPath":$one, "enSchematron":$two, "zatcaSchematron":$thr, "certPath":$fou, "privateKeyPath":$fiv, "pihPath":$six, "inputPath":$sev, "usagePathFile":$eight}' \
      > config.json && \
    cd ..

RUN chmod +x /SDK/zatca-einvoicing-sdk-238-R4.0.0/Apps/fatoora

# Copy the daemon binary from the builder stage
COPY --from=builder /daemon /usr/local/bin/daemon

# NATS port
EXPOSE 4222

# Default XML storage directory
VOLUME ["/data/zatca-xml"]

ENTRYPOINT ["daemon"]
