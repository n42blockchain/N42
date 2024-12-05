# N42  
N42 (N) is an implementation of a public blockchain (execution client) on the efficiency frontier, written in Go.

**Disclaimer: This software is currently a tech preview. We will do our best to keep it stable and avoid breaking changes, but we make no guarantees. Things can and will break.**

## System Requirements  

* For an Full node : ≥ 200 GB of storage space (SSD or NVMe recommended; HDD is not recommended).

* RAM: >=16GB, 64-bit architecture, [Golang version >= 1.19](https://golang.org/doc/install)


## Build from source code
For building the latest alpha release (this will be suitable for most users just wanting to run a node):

### Most Linux systems and macOS

Since n42 is written in Go, you'll need the latest version of Go installed to build it from source code.
Instructions for installing Go are available at the [Go installation page](https://golang.org/doc/install) and necessary bundles can be downloaded from the [Go download page](https://golang.org/dl/).
The repository should be cloned to a local environment. Once cloned, running the command `make n42` configures everything for a temporary build and then cleans up afterward. Please note that this build method is only compatible with UNIX-like operating systems.
```sh
git clone https://github.com/N42world/n42.git
cd n42
make n42
./build/bin/n42
```
### Windows

Windows users may run n42 in 3 possible ways:

* Build executable binaries natively for Windows using [Chocolatey package manager](https://chocolatey.org/)
* Use Docker :  see [docker-compose.yml](./docker-compose.yml)
* Use WSL (Windows Subsystem for Linux) **strictly on version 2**. Under this option you can build n42 just as you would on a regular Linux distribution. You can point your data also to any of the mounted Windows partitions (eg. `/mnt/c/[...]`, `/mnt/d/[...]` etc) but in such case be advised performance is impacted: this is due to the fact those mount points use `DrvFS` which is a [network file system](#blocks-execution-is-slow-on-cloud-network-drives) and, additionally, MDBX locks the db for exclusive access which implies only one process at a time can access data.  This has consequences on the running of `rpcdaemon` which has to be configured as [Remote DB](#for-remote-db) Even if it runs on the same computer, limitations may still apply. However, if your data is stored on the native Linux filesystem, these limitations do not apply. **Additionally, note that the default WSL2 environment has a separate IP address that differs from the Windows host’s network interface. Consider this when configuring NAT for port 30303 on your router.**

### Docker container
Docker allows for building and running n42 via containers. This alleviates the need for installing build dependencies onto the host OS.
see [docker-compose.yml](./docker-compose.yml) [dockerfile](./Dockerfile).
For convenience we provide the following commands:
```sh
make images # build docker images than contain executable n42 binaries
make up # alias for docker-compose up -d && docker-compose logs -f 
make down # alias for docker-compose down && clean docker data
make start #  alias for docker-compose start && docker-compose logs -f 
make stop # alias for docker-compose stop
```

## Executables

The N42 project comes with one wrappers/executables found in the `cmd`
directory.

|    Command    | Description                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                       |
| :-----------: | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
|  **`N42`**   | Our main N42 CLI client.  It can be used by other processes as a gateway into the N42 network via JSON RPC endpoints exposed on top of HTTP transports. `N42 --help`  for command line options.          |


## n42 ports

| Port  | Protocol |         Purpose          |       Expose       |
|:-----:|:--------:|:------------------------:|:------------------:|
| 61015 |   UDP    | The port used by discv5. |       Public       |
| 61016 |   TCP    | The port used by libp2p. |       Public       |
| 20012 |   TCP    |      Json rpc/HTTP       |       Public       |
| 20013 |   TCP    |    Json rpc/Websocket    |       Public       |
| 20014 |   TCP    | Json rpc/HTTP/Websocket  | JWT Authentication |
| 4000  |   TCP    |   BlockChain Explorer    |       Public       |
| 6060  |   TCP    |         Metrics          |      Private       | 
| 6060  |   TCP    |          Pprof           |      Private       | 

## License
The N42 library is licensed under the
[GNU General Public License v3.0](https://www.gnu.org/licenses/gpl-3.0.en.html).

