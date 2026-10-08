import { readFile } from 'node:fs/promises';
for (const name of ['index.html','app.js','style.css']) await readFile(new URL(name,import.meta.url));
console.log('发布器界面已就绪（直接嵌入独立应用）');
