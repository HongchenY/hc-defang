import { OpenAIStream, StreamingTextResponse } from "ai";
import { promises as fs } from "fs";
import { OpenAI } from "openai";

const openai = new OpenAI({
    baseURL: process.env.OPENAI_BASE_URL,
    apiKey: "",
});

export const POST = async function (req: Request) {
    const docs = await fs.readFile(process.cwd() + '/src/app/docs.md', 'utf8');

    const { messages } = await req.json();

    const response = await openai.chat.completions.create({
        model: "TheBloke/Mistral-7B-Instruct-v0.2-AWQ",
        stream: true,
        messages: [
            {
                role: "user",
                content: "Hello.",
            },
            {
                role: "assistant",
                content: (
                    `
I am a support rep for Defang.
Here is some more information about Defang:
----------------

${docs}

----------------
                    
If the above context does not give you the information you need to answer support questions, I will have to direct you to the Defang documentation at https://docs.defang.io/docs/intro
I will *always* answer you in 300 words or less. I promise.
                    `
                ),
            },
            ...messages,
        ],
        max_tokens: 500,
    });

    const stream = OpenAIStream(response);

    return new StreamingTextResponse(stream);
}