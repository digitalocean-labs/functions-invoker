# functions-invoker

This is a somewhat-[Apache OpenWhisk](https://github.com/apache/openwhisk)-compatible implementation of the **invoker** component in Golang. The invoker runs on a given node and orchestrates the function container lifecycle, executes functions in the respective containers, extracts responses and logs and more.

## Getting started

This repository is currently intended as an inspiration and potential base to be forked in order to gain upstream compatibility again. We have not closely tracked upstream Apache OpenWhisk and as such have currently not invested in a usable setup with the upstream codebase.

As currently implemented, the invoker makes the following assumptions about the deployment:
- Deployed in Kubernetes as a StatefulSet to guarantee ordinal hostnames to cooperate with the controller's loadbalancer.
- 1 invoker pod per node to give each respective invoker monopolized access to the respective node's resources.
- A Docker daemon running on each respective node, accessible to the invoker container via a host mount.
- CouchDB-based storage is implemented.
- The "classic" push-based loadbalancer is used.

### Limitations/Incompatibilities

Since we haven't tracked upstream's changes much, a few incompatibilities have crept in. There might be more, subtle incompatibilities so this is not an exhaustive list necessarily.

- Intra-container concurrency is not supported/implemented. Only one request per container per time is allowed.
- Only a very minimal subset of configuration is implemented.
    - The config surface for encryption keys was changed to facilitate multiple keys at a time for seamless rollover purposes.

## Improvements over the upstream implementation

### Invoking functions via HTTP

This invoker implementation allows for the controller to invoke functions via HTTP **and** Kafka. It is expected that HTTP is used when the controller's loadbalancer does not think the respective invoker is overloaded and that Kafka-based invocations are only used in overload cases. Thus, the vast majority of invocations, cut out transport via Kafka completely, greatly reducing the reliance on Kafka from an operational point-of-view and avoiding latency-related fine-tuning of Kafka completely.

Semantics of Kafka-based invocations are kept intact in that the invoker currently commits a Kafka message after it has successfully been written. A similar acknowledgement mechanism is implemented based on HTTP in that the HTTP response header is written as soon as the message has been received successfully and before the actual invocation is started. This is semantically the same signal as if the controller successfully committed the message to Kafka in a non-overloaded system, where it is assumed that the invoker reads and commits the message immediately as well. The invocation's finish is then marked with writing the HTTP response body and closing it. The contents of the HTTP request and response body are exactly the same as the message written into Kafka, so the same code can be reused for both variants. This makes the necessary changes to the controller fairly trivial:

```scala
if (!isOverloaded) {
  val uri = s"https://invoker0-blue-${invoker.toInt}.invoker0-blue.openwhisk.svc.cluster.local:8443/invoke"

  val hs = immutable.Seq(
    headers.RawHeader("Functions-Activation-Id", msg.activationId.asString),
    headers.RawHeader("Functions-Transaction-Id", msg.transid.id),
    headers.RawHeader("Functions-Account", msg.user.subject.asString),
    headers.RawHeader("Functions-Namespace", msg.user.namespace.name.asString),
    headers.RawHeader("Functions-Function", msg.action.name.asString))

  val resp = Http().singleRequest(HttpRequest(method = HttpMethods.POST,uri = uri, headers = hs, entity = msg.serialize), settings = connectionPoolSettings)
    .flatMap { resp =>
      resp.status match {
        case StatusCodes.OK => Future.successful(resp)
        case status => Future.failed(new IllegalStateException(s"unexpected status code from invoker: $status"))
      }
    }
    .andThen {
      case Failure(e) =>
        // If we fail to receive a successful response from the invoker, assume the function won't execute
        // and handle this as a system error.
        processCompletion(msg.activationId, transid, forced = true, isSystemError = true, invoker)
    }

  // This chain waits for the response to actually finish (i.e. the response body to be closed).
  // That is generally the signal that the respective workload is done.
  resp.foreach { resp =>
    // We only reach here if we've gotten a succesful response from the invoker.
    Unmarshal(resp.entity).to[Array[Byte]]
      .flatMap(processAcknowledgement)
      .andThen {
        case Failure(e) =>
          // If we failed to consume the response in some way, assume the function won't execute
          // and handle this as a system error.
          processCompletion(msg.activationId, transid, forced = true, isSystemError = true, invoker)
      }
  }

  // This waits for the invoker to accept the invocation. Note that it does **not** wait for the
  // response to be fully consumed. Thus, it indicates successful receipt only.
  resp.map(_ => ())
} else {
  val topic = target
  producer.send(topic, msg).map(_ => ())
}
```

### In-process docker client

In the upstream implementation, the `docker` binary is used to control the Docker daemon generally. Some optimizations have been implemented to speed up `docker pause` and `docker unpause` by directly using the `runc` binary.

Since this reimplementation was done in Golang, we took the opportunity to implement all Docker daemon related tasks via the native Golang client. This greatly reduced the overhead when dealing with Docker commands and allowed for more nuanced and informed error handling.

### In-process handling of runtime logs

In the upstream implementation, the logs of a runtime container are read via Docker's `json-file` log driver. The daemon essentially tails the stdout and stderr stream of the container, packs each individual line into a JSON structure, adds a timestamp and some more metadata and writes that to a file on the local disk. The file is tailed by the invoker and again read line-by-line until a log sentinel is reached marking that the respective stream has been flushed completely for the given invocation. The lines are then parsed and transformed into another format to then be stored with the activation record.

This implementation instead uses Docker's attach API to read a multiplexed bytestream of stdout/stderr directly. The container is configured with no log driver, so no logs ever hit the local disk. Instead, the bytestream is interpreted in-process and immediately transformed into the final format stored in the activation record.

This cut out a lot of the overhead of log collection and amortized it with the rest of the runtime lifecycle generally (log collection is in the double-digit microseconds now). It also cuts out the disk I/O overhead and any potential bugs that might arise because the underlying JSON file has to be rolled to avoid abusive logging to fill the respective disk.
