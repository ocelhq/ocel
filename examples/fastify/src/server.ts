import Fastify from 'fastify';
 
const fastify = Fastify({ logger: true });
 
fastify.get('/', async (request, reply) => {
  return { hello: 'world', status: 'ok' };
});
 
fastify.listen({ port: 3000 });