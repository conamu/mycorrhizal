# Mycorrizal
The embedded Library for scalable, modern and efficient modular Monoliths

Vision:
A distributed system, capable of basic messaging, caching and
storing small amounts of Persistent data in a remote location like S3.

It should cover leader election and distributed messaging and caching.
The leader Node should persist data to the chosen Filestorage.
For now this will be based on S3.

This library should make distributed service connections possible, enabling
a backbone for simple caching, storage and messaging to be able to build fast, scalable and efficient modular monoliths in Golang.
Consider each service instance to be a tree or a mushroom. This backbone is kind of the Mycelium underneath it.

## Components
### Nodosum
**The Layer of Co-ordination and Consensus** \
Nodosum is the part of funghi where Clamp connections are formed in some species.
This ensures stability, nutrition and for some species non-hierarchial leadership and consensus.
Nodosum is the component of Mycel that ensures a stable connected cluster of Hyphae.
Also ensuring leadership election for every subsystem individually so leadership of the whole Mycelium is not centralized
Anastomosis - term when a cluster is formed and consensus is being achieved

### Cytoplasm
**The Layer of Events and Messaging** \
Cytoplasm and Cytoplasmic streaming enables Continuous flow of nutrients, signaling molecules,
and even nuclei through the hyphal network. This is the event bus system of mycelium.

### Mycel
**The Layer of Caching and Storage** \
Mycel is the memory layer of a fungal network.
Septa partition hyphal compartments, like namespaces or buckets.
In this sense, Septa are the buckets of cache-data, Hyphae are the shards and
the Mycel is the networked caching and persistance layer
This name fits it pretty well since it is based on S3 protocol
for permanent storage and its datagram will be around buckets and namespaces.
The sharding is the partition of storage in hyphae,
distributing stored and cached data throughout the mycel across the cytoplasm network.

### Hypha
**The Layer of integration** \
A Hypha, or the Hyphae in a cluster are the nodes of the cluster network. 
Through Nodosum they self-discover and connect with each other 
to enable the other components to work and make Business logic of 
the implementing applicaiton interact with it

### Pulse
**The Layer of Interfacing** \
Pulse will be the CLI for this networked library.
Mycorrhizal networks are controlled by electrical and biochemical impulses.
In a way, we are controlling the Mycorrhizal Network via Pulses with Pulse!

# IDEAS

- Add advertisement/registering of services
- More efficient Network topology for cluster formation (cohorts of instances build a small star, multiple stars connect)