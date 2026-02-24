# THIS IS A WIP LEARNING PROJECT
(If you happen to stumble on this project lol)

I primarily wanted to build something unique, enabling ditributed monolithic architectures.
Turns out it was a great learning experience. Both in how caches and datastructures work, 
how hard networking can be in software development and why engineering decissions are taken the way they are nowadays.

Also I learned a lot about why we choose Valkey and Postgres for storage and caching for almost every project!

I have the uttermost respect for the people who build these systems.

All code in here was written by me. The last part, completing the rebalancer and some parts of the geospatial index cache have been written in assistance of Claude Code.
All the engineering effort, research, ideas, etc... where done by me. Claude assisted in writing the research documents in the Docs folder to save time on google.

It is usable, I tested it on a test application server. It ofcourse is nowhere near as fast as redis but its still usable to play around with or maybe prototype distributed systems.

Below the vision of this wild and weird project: I tried to model a leaderless distributed system after mycelium networks.

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
- structure nodosum -> commands,packet,acl -> cytoplasm,hypha,mycel,pulse
- when project is matured: introduce some sort of manifest like what functionalities the implementing software supports
  - this then can be used to build clusters of applications that are different from each other while still beeing able \n to access shared distributed cache and messaging making modular monoliths possible with microservices that can help out without the overhead of http